package admission

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
)

func fixture(t *testing.T, offline bool, max int64) (*Service, Pricing, Request) {
	t.Helper()
	s, _ := NewService(NewMemoryStore())
	c := Campaign{ID: "campaign", Profile: "fixture", Offline: offline, MaxSpendMicroUSD: max, MaxInputTokens: 10000, MaxRequests: 100}
	if err := s.CreateCampaign(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	p := Pricing{Version: "synthetic-v1", Provider: "fixture", Model: "fixture-model", Verified: true, InputBoundVerified: true, BillableOutputBound: true, InputMicroUSDPerMillion: 1_000_000, OutputMicroUSDPerMillion: 1_000_000}
	r := Request{ID: "request", CampaignID: c.ID, AttemptID: "attempt", Phase: "repair", ConfigVersion: "fixture-v1", PayloadHash: PayloadHash([]byte("fixture")), Provider: p.Provider, Model: p.Model, External: !offline, InputTokenBound: 10, OutputTokenBound: 5}
	return s, p, r
}
func TestBudgetSharedAcrossTriageRepairAndRestart(t *testing.T) {
	s, p, r := fixture(t, false, 30)
	ctx := context.Background()
	var admitted atomic.Int64
	var wg sync.WaitGroup
	for n := 0; n < 20; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			candidate := r
			candidate.ID = fmt.Sprintf("request-%d", n)
			candidate.AttemptID = fmt.Sprintf("attempt-%d", n)
			if n%2 == 0 {
				candidate.Phase = "triage"
			}
			_, err := s.Reserve(ctx, candidate, p)
			if err == nil {
				admitted.Add(1)
			} else if !errors.Is(err, ErrBudgetExceeded) {
				t.Errorf("unexpected reservation error: %v", err)
			}
		}(n)
	}
	wg.Wait()
	if admitted.Load() != 2 {
		t.Fatalf("admitted %d rather than shared ceiling 2", admitted.Load())
	}
	c, _ := s.GetCampaign(ctx, r.CampaignID)
	if c.ReservedMicroUSD != 30 || c.Requests != 2 {
		t.Fatalf("bad campaign: %+v", c)
	}
	if err := s.CreateCampaign(ctx, Campaign{ID: c.ID, Profile: c.Profile, MaxSpendMicroUSD: 31, MaxInputTokens: 10000, MaxRequests: 100}); !errors.Is(err, ErrMismatch) {
		t.Fatalf("campaign increased on recreation: %v", err)
	}
}
func TestBudgetRejectsUnknownPricingAndOfflineExternal(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		offline bool
		change  func(*Pricing, *Request)
		want    error
	}{
		{"unknown price", false, func(p *Pricing, _ *Request) { p.Verified = false }, ErrPricingUnknown},
		{"billable output", false, func(p *Pricing, _ *Request) { p.BillableOutputBound = false }, ErrBillingUnbounded},
		{"tokenizer bound", false, func(p *Pricing, _ *Request) { p.InputBoundVerified = false }, ErrBillingUnbounded},
		{"offline", true, func(_ *Pricing, r *Request) { r.External = true }, ErrOffline},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			s, p, r := fixture(t, scenario.offline, 100)
			scenario.change(&p, &r)
			if _, err := s.Reserve(context.Background(), r, p); !errors.Is(err, scenario.want) {
				t.Fatalf("error=%v want=%v", err, scenario.want)
			}
			c, _ := s.GetCampaign(context.Background(), r.CampaignID)
			if c.Requests != 0 {
				t.Fatal("rejection consumed request allowance")
			}
		})
	}
}
func TestResponseReceiptReplaysWithoutSecondDispatch(t *testing.T) {
	s, p, r := fixture(t, false, 100)
	ctx := context.Background()
	if _, err := s.Reserve(ctx, r, p); err != nil {
		t.Fatal(err)
	}
	if err := s.StartDispatch(ctx, r.ID, r.PayloadHash); err != nil {
		t.Fatal(err)
	}
	receipt := Receipt{RequestID: r.ID, PayloadHash: r.PayloadHash, Usage: Usage{Present: true, PromptTokens: 8, CompletionTokens: 3, TotalTokens: 11, CachedInputTokens: 4, ReasoningTokens: 2}, HTTPStatus: 200, Response: []byte("invalid JSON still billed"), ResponseEncoding: "fixture-raw"}
	if err := s.RecordResponse(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResponse(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	replayed, err := s.Reserve(ctx, r, p)
	if err != nil || replayed.State != Accounted || string(replayed.Receipt.Response) != string(receipt.Response) {
		t.Fatalf("replay: %+v %v", replayed, err)
	}
	if err := s.StartDispatch(ctx, r.ID, r.PayloadHash); !errors.Is(err, ErrAlreadyDispatched) {
		t.Fatalf("paid replay dispatched: %v", err)
	}
	c, _ := s.GetCampaign(ctx, r.CampaignID)
	if c.SpentMicroUSD != 11 || c.ReservedMicroUSD != 0 || c.Requests != 1 {
		t.Fatalf("double accounting %+v", c)
	}
	r.PayloadHash = PayloadHash([]byte("changed"))
	if _, err := s.Reserve(ctx, r, p); !errors.Is(err, ErrMismatch) {
		t.Fatalf("changed payload reused identity: %v", err)
	}
}
func TestMissingOrInvalidUsageBlocksNextPaidCall(t *testing.T) {
	for _, usage := range []Usage{{}, {Present: true, PromptTokens: 1, CompletionTokens: 1, TotalTokens: 3}, {Present: true, PromptTokens: 11, CompletionTokens: 1, TotalTokens: 12}, {Present: true, PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2, ReasoningTokens: 2}} {
		s, p, r := fixture(t, false, 100)
		ctx := context.Background()
		_, _ = s.Reserve(ctx, r, p)
		_ = s.StartDispatch(ctx, r.ID, r.PayloadHash)
		err := s.RecordResponse(ctx, Receipt{RequestID: r.ID, PayloadHash: r.PayloadHash, Usage: usage, HTTPStatus: 200, ResponseEncoding: "fixture-raw"})
		if !errors.Is(err, ErrUsageInvalid) {
			t.Fatalf("invalid usage: %v", err)
		}
		r.ID = "next"
		if _, err = s.Reserve(ctx, r, p); !errors.Is(err, ErrUncertain) {
			t.Fatalf("unknown usage permitted next call: %v", err)
		}
		c, _ := s.GetCampaign(ctx, r.CampaignID)
		if !c.Blocked || c.ReservedMicroUSD != 15 || c.SpentMicroUSD != 0 {
			t.Fatalf("uncertainty released allowance: %+v", c)
		}
	}
}
func TestDispatchCrashKeepsReservationAndCannotCancel(t *testing.T) {
	s, p, r := fixture(t, false, 100)
	ctx := context.Background()
	_, _ = s.Reserve(ctx, r, p)
	_ = s.StartDispatch(ctx, r.ID, r.PayloadHash)
	r.ID = "after-crash"
	if _, err := s.Reserve(ctx, r, p); !errors.Is(err, ErrUncertain) {
		t.Fatalf("inflight marker did not block replay: %v", err)
	}
	if err := s.CancelReserved(ctx, "request", r.PayloadHash); !errors.Is(err, ErrAlreadyDispatched) {
		t.Fatalf("dispatch released: %v", err)
	}
	if err := s.MarkUncertain(ctx, "request", r.PayloadHash); err != nil {
		t.Fatal(err)
	}
}
func TestCostRoundingOverflowAndConservativeUTF8Bound(t *testing.T) {
	if cost, err := Cost(Pricing{InputMicroUSDPerMillion: 1, OutputMicroUSDPerMillion: 1, FixedRequestMicroUSD: 2}, 1, 1); err != nil || cost != 4 {
		t.Fatalf("rounded cost %d %v", cost, err)
	}
	if _, err := Cost(Pricing{InputMicroUSDPerMillion: math.MaxInt64}, 2, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("overflow accepted: %v", err)
	}
	payload := []byte(`{"text":"🤖漢字","tools":[{"description":"bounded"}]}`)
	bound, err := ConservativeInputBound(payload, 128)
	if err != nil || bound != int64(len(payload))+128 {
		t.Fatalf("bound=%d %v", bound, err)
	}
}

func TestBudgetRejectsCredentialBearingMetadata(t *testing.T) {
	s, p, r := fixture(t, false, 100)
	r.ConfigVersion = "https://fixture.invalid?api_key=private-value"
	if _, err := s.Reserve(context.Background(), r, p); !errors.Is(err, ErrInvalid) {
		t.Fatalf("credential-bearing metadata accepted: %v", err)
	}
	c, _ := s.GetCampaign(context.Background(), r.CampaignID)
	if c.Requests != 0 {
		t.Fatal("sensitive metadata persisted a reservation")
	}
}

func TestBudgetDispatchMarkerIsOneUseUnderConcurrency(t *testing.T) {
	s, p, r := fixture(t, false, 100)
	ctx := context.Background()
	if _, err := s.Reserve(ctx, r, p); err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for n := 0; n < 16; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.StartDispatch(ctx, r.ID, r.PayloadHash)
			if err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, ErrAlreadyDispatched) {
				t.Errorf("dispatch: %v", err)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("dispatch marker granted %d callers", accepted.Load())
	}
}
