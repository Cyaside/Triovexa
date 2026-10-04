package modelgateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Cyaside/Triovexa/internal/ai/admission"
)

func TestContextLimitIsTypedAndDeniedBeforeAdmission(t *testing.T) {
	var calls atomic.Int32
	gateway, ledger := gatewayFixture(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	body := modelPayload(strings.Repeat("x", 7000))
	if _, _, _, err := gateway.DispatchAt(context.Background(), 1, body); !errors.Is(err, ErrContextLimit) {
		t.Fatalf("context admission lost its typed error: %v", err)
	}
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, privateRequest(gateway, body))
	campaign, err := ledger.GetCampaign(context.Background(), gateway.config.CampaignID)
	if err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusConflict || response.Body.String() != "CONTEXT_LIMIT\n" || calls.Load() != 0 ||
		campaign.Requests != 0 || campaign.ReservedMicroUSD != 0 || campaign.AdmittedInputTokens != 0 {
		t.Fatalf("context rejection: status=%d body=%q calls=%d campaign=%+v", response.Code, response.Body.String(), calls.Load(), campaign)
	}
}

func TestPrivateDenialCodesNeverExposeUnderlyingErrorText(t *testing.T) {
	for _, scenario := range []struct {
		err  error
		code string
	}{
		{ErrContextLimit, "CONTEXT_LIMIT"}, {admission.ErrOffline, "OFFLINE_EGRESS_DENIED"},
		{admission.ErrPricingUnknown, "PRICING_UNKNOWN"}, {admission.ErrBillingUnbounded, "BILLING_UNBOUNDED"},
		{admission.ErrBudgetExceeded, "BUDGET_EXHAUSTED"}, {admission.ErrUncertain, "PROVIDER_DISPATCH_UNCERTAIN"},
		{admission.ErrUsageInvalid, "USAGE_UNKNOWN"}, {errors.New("private-provider-material"), "MODEL_DISPATCH_BLOCKED"},
	} {
		err := fmt.Errorf("private-provider-material: %w", scenario.err)
		if code := denialCode(err); code != scenario.code || strings.Contains(code, "private-provider-material") {
			t.Fatalf("unsafe denial code: got %q want %q", code, scenario.code)
		}
	}
}
