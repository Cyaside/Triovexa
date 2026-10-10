package admission

import (
	"context"
	"errors"
	"testing"
)

func TestProviderManagedAccountingPreservesReceiptsAndUncertainty(t *testing.T) {
	ctx := context.Background()
	s, p, r := fixture(t, false, 30)
	c := Campaign{ID: "provider-managed", Profile: "internal", ProviderManaged: true}
	if err := s.CreateCampaign(ctx, c); err != nil {
		t.Fatal(err)
	}
	r.CampaignID = c.ID
	for _, id := range []string{"first", "second", "third"} {
		r.ID = id
		if _, err := s.Reserve(ctx, r, p); err != nil {
			t.Fatal(err)
		}
		if err := s.StartDispatch(ctx, r.ID, r.PayloadHash); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordResponse(ctx, Receipt{RequestID: r.ID, PayloadHash: r.PayloadHash, HTTPStatus: 200, Response: []byte("private-receipt"), ResponseEncoding: "fixture-raw", Usage: Usage{Present: true, PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}}); err != nil {
			t.Fatal(err)
		}
	}
	retained, _ := s.GetCampaign(ctx, c.ID)
	if retained.Requests != 3 || retained.SpentMicroUSD != 45 || retained.AdmittedInputTokens != 30 || retained.ReservedMicroUSD != 0 {
		t.Fatalf("accounting lost: %+v", retained)
	}
	if replay, err := s.Reserve(ctx, r, p); err != nil || replay.State != Accounted {
		t.Fatalf("replay lost: %v", err)
	}
	r.ID = "uncertain"
	if _, err := s.Reserve(ctx, r, p); err != nil {
		t.Fatal(err)
	}
	if err := s.StartDispatch(ctx, r.ID, r.PayloadHash); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkUncertain(ctx, r.ID, r.PayloadHash); err != nil {
		t.Fatal(err)
	}
	r.ID = "must-not-dispatch"
	if _, err := s.Reserve(ctx, r, p); !errors.Is(err, ErrUncertain) {
		t.Fatalf("uncertainty bypassed: %v", err)
	}
	c.ProviderManaged = false
	c.MaxRequests = 6
	c.MaxInputTokens = 12000
	c.MaxSpendMicroUSD = 200000
	if err := s.CreateCampaign(ctx, c); !errors.Is(err, ErrMismatch) {
		t.Fatalf("mode changed on existing campaign: %v", err)
	}
}

func TestProviderManagedRequiresExplicitInternalProfile(t *testing.T) {
	s, _ := NewService(NewMemoryStore())
	for _, c := range []Campaign{
		{ID: "smoke", Profile: "final-smoke", ProviderManaged: true},
		{ID: "offline", Profile: "internal", ProviderManaged: true, Offline: true},
		{ID: "mixed", Profile: "internal", ProviderManaged: true, MaxRequests: 1},
	} {
		if err := s.CreateCampaign(context.Background(), c); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid mode accepted: %v", err)
		}
	}
}
