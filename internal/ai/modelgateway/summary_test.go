package modelgateway

import (
	"context"
	"testing"

	"github.com/Cyaside/Triovexa/internal/ai/admission"
)

func TestLedgerSummaryDoesNotReportCancelledReservationAsModelUsage(t *testing.T) {
	ctx := context.Background()
	ledger, _ := admission.NewService(admission.NewMemoryStore())
	if err := ledger.CreateCampaign(ctx, admission.Campaign{ID: "campaign", Profile: "offline-fixture", Offline: true, MaxSpendMicroUSD: 100, MaxInputTokens: 100, MaxRequests: 3}); err != nil {
		t.Fatal(err)
	}
	p := admission.Pricing{Version: "synthetic-v1", Provider: "fixture", Model: "fixture", InputMicroUSDPerMillion: 1000000, OutputMicroUSDPerMillion: 1000000}
	r := admission.Request{ID: "campaign:attempt:repair:1", CampaignID: "campaign", AttemptID: "attempt", Phase: "repair", ConfigVersion: "fixture-v1", PayloadHash: admission.PayloadHash([]byte("fixture")), Provider: "fixture", Model: "fixture", InputTokenBound: 10, OutputTokenBound: 5}
	if _, err := ledger.Reserve(ctx, r, p); err != nil {
		t.Fatal(err)
	}
	s, err := ReadSummary(ctx, ledger, "campaign", "attempt", "repair", 3)
	if err != nil || s.ModelRequests != 0 || s.ReservedMicroUSD != 15 {
		t.Fatal("unsent reservation falsely counted as model dispatch", s, err)
	}
	if err := ledger.CancelReserved(ctx, r.ID, r.PayloadHash); err != nil {
		t.Fatal(err)
	}
	s, err = ReadSummary(ctx, ledger, "campaign", "attempt", "repair", 3)
	if err != nil || s.ModelRequests != 0 || s.ReservedMicroUSD != 0 || s.AccountedMicroUSD != 0 || s.InputTokenBound != 0 || s.UsageStatus != "not_requested" {
		t.Fatal("cancelled request falsely billed", s, err)
	}
}
