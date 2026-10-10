package modelgateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/ai/admission"
)

func TestBudgetSharedAcrossTriageRepairAndRestart(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"model":"fixture-model","choices":[],"usage":{"prompt_tokens":20,"completion_tokens":10,"total_tokens":30}}`)
	}))
	defer server.Close()
	gateway, ledger := gatewayFixture(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unused upstream") })
	config := BudgetConfig{Campaign: admission.Campaign{ID: "shared-offline", Profile: "offline-fixture", Offline: true, MaxSpendMicroUSD: 200000, MaxInputTokens: 12000, MaxRequests: 2}, Pricing: gateway.config.Pricing, ConfigVersion: "shared-config-v1", MaxInputTokens: 6000, MaxOutputTokens: 1500}
	dispatcher, err := NewSharedDispatcher(context.Background(), config, ledger, gateway.config.Cipher)
	if err != nil {
		t.Fatal(err)
	}
	provider := gateway.config.Provider
	provider.BaseURL = server.URL
	for _, phase := range []string{"triage", "remediation"} {
		ctx := ai.WithRequestScope(context.Background(), ai.RequestScope{RunID: "job-1", Phase: phase, Ordinal: 1})
		if _, _, _, err := dispatcher.Dispatch(ctx, provider, modelPayload("test")); err != nil {
			t.Fatal(err)
		}
	}
	restarted, err := NewSharedDispatcher(context.Background(), config, ledger, gateway.config.Cipher)
	if err != nil {
		t.Fatal(err)
	}
	ctx := ai.WithRequestScope(context.Background(), ai.RequestScope{RunID: "new-repair-attempt", Phase: "repair", Ordinal: 1})
	if _, _, _, err := restarted.Dispatch(ctx, provider, modelPayload("test")); !errors.Is(err, admission.ErrBudgetExceeded) || calls.Load() != 2 {
		t.Fatalf("new component/restart reset campaign allowance: calls=%d err=%v", calls.Load(), err)
	}
}

func TestUnknownPricingRejectsBeforeNetwork(t *testing.T) {
	gateway, _ := gatewayFixture(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unknown pricing dispatched") })
	// An HTTPS external endpoint is rejected before resolving or connecting.
	gateway.config.Provider.BaseURL = "https://provider.example.invalid/v1"
	ledger, _ := admission.NewService(admission.NewMemoryStore())
	if err := ledger.CreateCampaign(context.Background(), admission.Campaign{ID: "paid", Profile: "final-smoke", MaxRequests: 6, MaxSpendMicroUSD: 200000, MaxInputTokens: 12000}); err != nil {
		t.Fatal(err)
	}
	gateway.ledger = ledger
	gateway.config.CampaignID = "paid"
	if _, _, _, err := gateway.DispatchAt(context.Background(), 1, modelPayload("test")); !errors.Is(err, admission.ErrPricingUnknown) {
		t.Fatalf("unknown pricing did not block: %v", err)
	}
	gateway.config.Pricing.Verified = true
	if _, _, _, err := gateway.DispatchAt(context.Background(), 1, modelPayload("test")); !errors.Is(err, admission.ErrBillingUnbounded) {
		t.Fatalf("unverified billing bound did not block: %v", err)
	}
}

func TestBudgetConfigCannotRaiseFinalSmokeLimits(t *testing.T) {
	base := BudgetConfig{Campaign: admission.Campaign{ID: "smoke", Profile: "final-smoke", MaxRequests: 6, MaxSpendMicroUSD: 200000, MaxInputTokens: 12000}, ConfigVersion: "v1", MaxInputTokens: 6000, MaxOutputTokens: 1500}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, modify := range []func(*BudgetConfig){
		func(c *BudgetConfig) { c.Campaign.ProviderManaged = true },
		func(c *BudgetConfig) { c.Campaign.MaxSpendMicroUSD++ }, func(c *BudgetConfig) { c.Campaign.MaxRequests++ },
		func(c *BudgetConfig) { c.Campaign.MaxInputTokens++ }, func(c *BudgetConfig) { c.MaxInputTokens++ }, func(c *BudgetConfig) { c.MaxOutputTokens++ },
	} {
		changed := base
		modify(&changed)
		if changed.Validate() == nil {
			t.Fatal("final smoke cap could be raised")
		}
	}
}

func TestProviderManagedConfigKeepsExecutionLimits(t *testing.T) {
	c := BudgetConfig{Campaign: admission.Campaign{ID: "internal", Profile: "internal", ProviderManaged: true}, ConfigVersion: "v1", MaxInputTokens: 65536, MaxOutputTokens: 4096}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	d := &SharedDispatcher{config: c}
	if d.requestLimit() != 20 {
		t.Fatal("workflow loop guard lost")
	}
	c.Campaign.MaxRequests = 1
	if c.Validate() == nil {
		t.Fatal("mixed accounting modes accepted")
	}
}
