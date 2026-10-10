package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Cyaside/Triovexa/internal/ai/admission"
)

func TestProviderManagedPostgresAccountingAndRestartIntegration(t *testing.T) {
	store, dsn, _ := budgetIntegrationStore(t)
	ctx := context.Background()
	service, pricing, request := budgetIntegrationFixture(t, store, 15)
	// The capped campaign remains immutable and enforced next to the new mode.
	original := request.CampaignID
	managed := admission.Campaign{ID: "provider-managed", Profile: "internal", ProviderManaged: true}
	if err := service.CreateCampaign(ctx, managed); err != nil {
		t.Fatal(err)
	}
	request.CampaignID = managed.ID
	for ordinal := 1; ordinal <= 5; ordinal++ {
		request.ID = fmt.Sprintf("managed-%d", ordinal)
		if _, err := service.Reserve(ctx, request, pricing); err != nil {
			t.Fatal(err)
		}
		if err := service.StartDispatch(ctx, request.ID, request.PayloadHash); err != nil {
			t.Fatal(err)
		}
		receipt := admission.Receipt{RequestID: request.ID, PayloadHash: request.PayloadHash, HTTPStatus: 200, ResponseEncoding: "sealed-v1", Response: []byte("retained-receipt"), Usage: admission.Usage{Present: true, PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}}
		if err := service.RecordResponse(ctx, receipt); err != nil {
			t.Fatal(err)
		}
	}
	restarted, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	after, _ := admission.NewService(restarted.ModelBudgetStore())
	c, err := after.GetCampaign(ctx, managed.ID)
	if err != nil || !c.ProviderManaged || c.Requests != 5 || c.SpentMicroUSD != 75 || c.AdmittedInputTokens != 50 || c.ReservedMicroUSD != 0 {
		t.Fatalf("retained accounting: %+v %v", c, err)
	}
	if replay, err := after.Reserve(ctx, request, pricing); err != nil || replay.State != admission.Accounted || string(replay.Receipt.Response) != "retained-receipt" {
		t.Fatalf("receipt replay: %v", err)
	}
	managed.ProviderManaged = false
	managed.MaxInputTokens = 12000
	managed.MaxRequests = 6
	managed.MaxSpendMicroUSD = 200000
	if err := after.CreateCampaign(ctx, managed); !errors.Is(err, admission.ErrMismatch) {
		t.Fatalf("mode changed after restart: %v", err)
	}
	request.CampaignID = original
	request.ID = "capped-first"
	if _, err := after.Reserve(ctx, request, pricing); err != nil {
		t.Fatal(err)
	}
	request.ID = "capped-second"
	if _, err := after.Reserve(ctx, request, pricing); !errors.Is(err, admission.ErrBudgetExceeded) {
		t.Fatalf("original cap lost: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO ai_budget_campaigns(id,profile,offline,provider_managed,max_spend_micro_usd,max_input_tokens,max_requests,created_at) VALUES('invalid','final-smoke',false,true,0,0,0,now())`); err == nil {
		t.Fatal("database accepted provider-managed final-smoke")
	}
}
