package admission

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestRepeatedPromptsConsumeCumulativeInputAndRequestAllowances(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	service, _ := NewService(store)
	if err := service.CreateCampaign(ctx, Campaign{ID: "bounded", Profile: "final-smoke", MaxSpendMicroUSD: 200000, MaxInputTokens: 12000, MaxRequests: 6}); err != nil {
		t.Fatal(err)
	}
	pricing := Pricing{Version: "synthetic-v1", Provider: "fixture", Model: "fixture-model", Verified: true, InputBoundVerified: true, BillableOutputBound: true,
		InputMicroUSDPerMillion: 1_000_000, OutputMicroUSDPerMillion: 1_000_000}
	for n, phase := range []string{"triage", "repair"} {
		request := Request{ID: fmt.Sprintf("request-%d", n), CampaignID: "bounded", AttemptID: "attempt", Phase: phase, ConfigVersion: "config-v1",
			PayloadHash: PayloadHash([]byte("repeated serialized context")), Provider: pricing.Provider, Model: pricing.Model,
			External: true, InputTokenBound: 6000, OutputTokenBound: 1500}
		if _, err := service.Reserve(ctx, request, pricing); err != nil {
			t.Fatal(err)
		}
		if err := service.StartDispatch(ctx, request.ID, request.PayloadHash); err != nil {
			t.Fatal(err)
		}
		if err := service.RecordResponse(ctx, Receipt{RequestID: request.ID, PayloadHash: request.PayloadHash, HTTPStatus: 200,
			ResponseEncoding: "fixture-raw", Usage: Usage{Present: true, PromptTokens: 1000, CompletionTokens: 10, TotalTokens: 1010}}); err != nil {
			t.Fatal(err)
		}
	}
	// Recreating the service keeps the campaign and all serialized prompt bounds;
	// low actual usage does not silently grant more context than the hard cap.
	restarted, _ := NewService(store)
	request := Request{ID: "after-restart", CampaignID: "bounded", AttemptID: "new-attempt", Phase: "repair", ConfigVersion: "config-v1",
		PayloadHash: PayloadHash([]byte("next prompt")), Provider: pricing.Provider, Model: pricing.Model, External: true, InputTokenBound: 1, OutputTokenBound: 1}
	if _, err := restarted.Reserve(ctx, request, pricing); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("cumulative serialized input allowance reset: %v", err)
	}
	campaign, _ := restarted.GetCampaign(ctx, "bounded")
	if campaign.AdmittedInputTokens != 12000 || campaign.Requests != 2 || campaign.SpentMicroUSD != 2020 || campaign.ReservedMicroUSD != 0 {
		t.Fatalf("incorrect cumulative accounting: %+v", campaign)
	}
}

func TestUncertainDispatchBlocksEveryComponentAndAttempt(t *testing.T) {
	service, pricing, request := fixture(t, false, 100)
	ctx := context.Background()
	if _, err := service.Reserve(ctx, request, pricing); err != nil {
		t.Fatal(err)
	}
	if err := service.StartDispatch(ctx, request.ID, request.PayloadHash); err != nil {
		t.Fatal(err)
	}
	if err := service.MarkUncertain(ctx, request.ID, request.PayloadHash); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"triage", "remediation", "repair", "connection-check"} {
		next := request
		next.ID, next.AttemptID, next.Phase = "next-"+phase, "fresh-attempt", phase
		if _, err := service.Reserve(ctx, next, pricing); !errors.Is(err, ErrUncertain) {
			t.Fatalf("uncertain dispatch escaped through %s: %v", phase, err)
		}
	}
	campaign, _ := service.GetCampaign(ctx, request.CampaignID)
	if campaign.Requests != 1 || campaign.ReservedMicroUSD != 15 || !campaign.Blocked {
		t.Fatalf("uncertainty released campaign allowance: %+v", campaign)
	}
}

func TestCancelledReservationRefundsOnlyBeforeDispatchAndIdentityRemainsSpent(t *testing.T) {
	service, pricing, request := fixture(t, false, 100)
	ctx := context.Background()
	if _, err := service.Reserve(ctx, request, pricing); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		if err := service.CancelReserved(ctx, request.ID, request.PayloadHash); err != nil {
			t.Fatal(err)
		}
	}
	campaign, _ := service.GetCampaign(ctx, request.CampaignID)
	if campaign.Requests != 0 || campaign.AdmittedInputTokens != 0 || campaign.ReservedMicroUSD != 0 {
		t.Fatalf("cancelled reservation was not refunded exactly once: %+v", campaign)
	}
	if _, err := service.Reserve(ctx, request, pricing); !errors.Is(err, ErrAlreadyDispatched) {
		t.Fatalf("cancelled request identity was silently reused: %v", err)
	}
	request.ID = "fresh-identity"
	if _, err := service.Reserve(ctx, request, pricing); err != nil {
		t.Fatal(err)
	}
	if err := service.StartDispatch(ctx, request.ID, request.PayloadHash); err != nil {
		t.Fatal(err)
	}
	if err := service.CancelReserved(ctx, request.ID, request.PayloadHash); !errors.Is(err, ErrAlreadyDispatched) {
		t.Fatalf("dispatch marker was refunded: %v", err)
	}
}
