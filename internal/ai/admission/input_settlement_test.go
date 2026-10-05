package admission

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestKnownInputUsageSettlesAllowanceWithoutChangingRequestBound(t *testing.T) {
	service, pricing, request := fixture(t, false, 20000)
	ctx := context.Background()
	_, _ = service.Reserve(ctx, request, pricing)
	_ = service.StartDispatch(ctx, request.ID, request.PayloadHash)
	receipt := Receipt{RequestID: request.ID, PayloadHash: request.PayloadHash, HTTPStatus: 200,
		ResponseEncoding: "fixture-raw", Usage: Usage{Present: true, PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5}}
	for n := 0; n < 2; n++ {
		if err := service.RecordResponse(ctx, receipt); err != nil {
			t.Fatal(err)
		}
	}
	campaign, _ := service.GetCampaign(ctx, request.CampaignID)
	if campaign.AdmittedInputTokens != 2 || campaign.Requests != 1 || campaign.SpentMicroUSD != 5 || campaign.ReservedMicroUSD != 0 {
		t.Fatalf("usage did not settle once: %+v", campaign)
	}
	stored, _ := service.GetRequest(ctx, request.ID)
	if stored.Request != request || stored.State != Accounted || stored.Receipt.Usage != receipt.Usage {
		t.Fatalf("original request or known receipt changed: %+v", stored)
	}
	// The settled contribution plus the entire next bound must fit before dispatch.
	next := request
	next.ID = "next"
	next.InputTokenBound = 9998
	if _, err := service.Reserve(ctx, next, pricing); err != nil {
		t.Fatalf("validated unused allowance unavailable: %v", err)
	}
	next.ID, next.InputTokenBound = "over-cap", 1
	if _, err := service.Reserve(ctx, next, pricing); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("unsettled bound did not enforce cumulative cap: %v", err)
	}
}

func TestInputSettlementAndConcurrentReservationsPreserveCeiling(t *testing.T) {
	ctx := context.Background()
	service, _ := NewService(NewMemoryStore())
	if err := service.CreateCampaign(ctx, Campaign{ID: "small", Profile: "fixture", MaxInputTokens: 100, MaxRequests: 20, MaxSpendMicroUSD: 10000}); err != nil {
		t.Fatal(err)
	}
	pricing := Pricing{Version: "synthetic-v1", Provider: "fixture", Model: "fixture", Verified: true, InputBoundVerified: true, BillableOutputBound: true}
	request := Request{ID: "first", CampaignID: "small", AttemptID: "attempt", Phase: "repair", ConfigVersion: "v1",
		PayloadHash: PayloadHash([]byte("fixture")), Provider: "fixture", Model: "fixture", External: true, InputTokenBound: 60, OutputTokenBound: 1}
	if _, err := service.Reserve(ctx, request, pricing); err != nil {
		t.Fatal(err)
	}
	if err := service.StartDispatch(ctx, request.ID, request.PayloadHash); err != nil {
		t.Fatal(err)
	}
	receipt := Receipt{RequestID: request.ID, PayloadHash: request.PayloadHash, HTTPStatus: 200, ResponseEncoding: "fixture-raw",
		Usage: Usage{Present: true, PromptTokens: 20, TotalTokens: 20}}
	var wg sync.WaitGroup
	// Concurrent receipt replays cannot release the same allowance twice.
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := service.RecordResponse(ctx, receipt); err != nil {
				t.Errorf("settlement replay: %v", err)
			}
		}()
	}
	wg.Wait()
	var accepted atomic.Int64
	for n := 0; n < 16; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			next := request
			next.ID, next.InputTokenBound = fmt.Sprintf("next-%d", n), 40
			if _, err := service.Reserve(ctx, next, pricing); err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, ErrBudgetExceeded) {
				t.Errorf("reservation: %v", err)
			}
		}(n)
	}
	wg.Wait()
	campaign, _ := service.GetCampaign(ctx, request.CampaignID)
	if accepted.Load() != 2 || campaign.AdmittedInputTokens != 100 || campaign.Requests != 3 {
		t.Fatalf("settlement expanded cap: accepted=%d campaign=%+v", accepted.Load(), campaign)
	}
}

func TestKnownReceiptSettlesInputButDoesNotClearUncertaintyBlock(t *testing.T) {
	service, pricing, request := fixture(t, false, 100)
	ctx := context.Background()
	_, _ = service.Reserve(ctx, request, pricing)
	_ = service.StartDispatch(ctx, request.ID, request.PayloadHash)
	_ = service.MarkUncertain(ctx, request.ID, request.PayloadHash)
	if err := service.RecordResponse(ctx, Receipt{RequestID: request.ID, PayloadHash: request.PayloadHash, HTTPStatus: 200,
		ResponseEncoding: "fixture-raw", Usage: Usage{Present: true, PromptTokens: 2, TotalTokens: 2}}); err != nil {
		t.Fatal(err)
	}
	campaign, _ := service.GetCampaign(ctx, request.CampaignID)
	if !campaign.Blocked || campaign.AdmittedInputTokens != 2 || campaign.Requests != 1 {
		t.Fatalf("settlement removed safety block: %+v", campaign)
	}
	request.ID = "after-known-receipt"
	if _, err := service.Reserve(ctx, request, pricing); !errors.Is(err, ErrUncertain) {
		t.Fatalf("accounting silently authorized retry: %v", err)
	}
}

func TestInputSettlementStoreRejectsInvalidKnownUsage(t *testing.T) {
	for _, usage := range []Usage{
		{},
		{Present: true, PromptTokens: 11, TotalTokens: 11},
		{Present: true, CompletionTokens: 6, TotalTokens: 6},
		{Present: true, PromptTokens: 1, TotalTokens: 2},
	} {
		service, pricing, request := fixture(t, false, 100)
		ctx := context.Background()
		_, _ = service.Reserve(ctx, request, pricing)
		_ = service.StartDispatch(ctx, request.ID, request.PayloadHash)
		// The store also checks the bound when its caller labels a receipt valid.
		err := service.store.RecordResponse(ctx, Receipt{RequestID: request.ID, PayloadHash: request.PayloadHash, HTTPStatus: 200,
			ResponseEncoding: "fixture-raw", Usage: usage}, 0, true)
		if !errors.Is(err, ErrUsageInvalid) {
			t.Fatalf("invalid settlement accepted: %v", err)
		}
		campaign, _ := service.GetCampaign(ctx, request.CampaignID)
		stored, _ := service.GetRequest(ctx, request.ID)
		if campaign.AdmittedInputTokens != request.InputTokenBound || campaign.ReservedMicroUSD != 15 || stored.Receipt != nil || stored.State != Dispatching {
			t.Fatalf("invalid receipt changed accounting: campaign=%+v request=%+v", campaign, stored)
		}
		request.ID = "after-invalid"
		if _, err := service.Reserve(ctx, request, pricing); !errors.Is(err, ErrUncertain) {
			t.Fatalf("invalid store settlement allowed dispatch: %v", err)
		}
	}
}
