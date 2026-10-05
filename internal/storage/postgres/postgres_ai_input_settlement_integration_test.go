package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Cyaside/Triovexa/internal/ai/admission"
	"github.com/google/uuid"
)

func inputSettlementFixture(t *testing.T) (*PostgresStore, *admission.Service, admission.Pricing, admission.Request) {
	t.Helper()
	store, _, _ := budgetIntegrationStore(t)
	service, _ := admission.NewService(store.ModelBudgetStore())
	campaign := admission.Campaign{ID: uuid.NewString(), Profile: "synthetic-integration", MaxInputTokens: 12000, MaxRequests: 6, MaxSpendMicroUSD: 200000}
	if err := service.CreateCampaign(context.Background(), campaign); err != nil {
		t.Fatal(err)
	}
	pricing := admission.Pricing{Version: "synthetic-v1", Provider: "fixture", Model: "fixture", Verified: true, InputBoundVerified: true, BillableOutputBound: true,
		InputMicroUSDPerMillion: 1_000_000, OutputMicroUSDPerMillion: 1_000_000}
	request := admission.Request{ID: uuid.NewString(), CampaignID: campaign.ID, AttemptID: uuid.NewString(), Phase: "repair", ConfigVersion: "fixture-v1",
		PayloadHash: admission.PayloadHash([]byte("synthetic input")), Provider: pricing.Provider, Model: pricing.Model, External: true, InputTokenBound: 6000, OutputTokenBound: 1}
	return store, service, pricing, request
}

func settledInputReceipt(t *testing.T, service *admission.Service, pricing admission.Pricing, request admission.Request) admission.Receipt {
	t.Helper()
	ctx := context.Background()
	if _, err := service.Reserve(ctx, request, pricing); err != nil {
		t.Fatal(err)
	}
	if err := service.StartDispatch(ctx, request.ID, request.PayloadHash); err != nil {
		t.Fatal(err)
	}
	receipt := admission.Receipt{RequestID: request.ID, PayloadHash: request.PayloadHash, HTTPStatus: 200, ResponseEncoding: "sealed-v1",
		Usage: admission.Usage{Present: true, PromptTokens: 1000, CompletionTokens: 1, TotalTokens: 1001}}
	if err := service.RecordResponse(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestBudgetPostgresInputSettlementAndConcurrentReservationsIntegration(t *testing.T) {
	_, service, pricing, request := inputSettlementFixture(t)
	ctx := context.Background()
	receipt := settledInputReceipt(t, service, pricing, request)
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := service.RecordResponse(ctx, receipt); err != nil {
				t.Errorf("receipt replay: %v", err)
			}
		}()
	}
	var accepted atomic.Int64
	for n := 0; n < 16; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			next := request
			next.ID, next.InputTokenBound = fmt.Sprintf("%s-next-%d", request.ID, n), 5500
			if _, err := service.Reserve(ctx, next, pricing); err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, admission.ErrBudgetExceeded) {
				t.Errorf("reservation: %v", err)
			}
		}(n)
	}
	wg.Wait()
	campaign, err := service.GetCampaign(ctx, request.CampaignID)
	if err != nil || accepted.Load() != 2 || campaign.AdmittedInputTokens != 12000 || campaign.Requests != 3 || campaign.SpentMicroUSD != 1001 || campaign.ReservedMicroUSD != 11002 {
		t.Fatalf("receipt/reservation transaction exceeded cap: accepted=%d campaign=%+v err=%v", accepted.Load(), campaign, err)
	}
	stored, err := service.GetRequest(ctx, request.ID)
	if err != nil || stored.Request != request || stored.State != admission.Accounted || stored.Receipt.Usage != receipt.Usage {
		t.Fatalf("original bound or known receipt changed: %+v %v", stored, err)
	}
}

func TestBudgetPostgresInputSettlementMigrationRetainsUnresolvedAllowancesIntegration(t *testing.T) {
	store, service, pricing, request := inputSettlementFixture(t)
	ctx := context.Background()
	receipt := settledInputReceipt(t, service, pricing, request)
	cancelled := request
	cancelled.ID, cancelled.InputTokenBound = uuid.NewString(), 500
	if _, err := service.Reserve(ctx, cancelled, pricing); err != nil {
		t.Fatal(err)
	}
	if err := service.CancelReserved(ctx, cancelled.ID, cancelled.PayloadHash); err != nil {
		t.Fatal(err)
	}
	reserved := request
	reserved.ID, reserved.InputTokenBound = uuid.NewString(), 2000
	if _, err := service.Reserve(ctx, reserved, pricing); err != nil {
		t.Fatal(err)
	}
	uncertain := request
	uncertain.ID, uncertain.InputTokenBound = uuid.NewString(), 1000
	if _, err := service.Reserve(ctx, uncertain, pricing); err != nil {
		t.Fatal(err)
	}
	if err := service.StartDispatch(ctx, uncertain.ID, uncertain.PayloadHash); err != nil {
		t.Fatal(err)
	}
	if err := service.MarkUncertain(ctx, uncertain.ID, uncertain.PayloadHash); err != nil {
		t.Fatal(err)
	}
	want, _ := service.GetCampaign(ctx, request.CampaignID)
	// Only this UUID test schema is changed to reproduce pre-010 accounting.
	if _, err := store.db.Exec(`UPDATE ai_budget_campaigns SET admitted_input_tokens=9000 WHERE id=$1`, request.CampaignID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM schema_migrations WHERE version='010_ai_input_settlement'`); err != nil {
		t.Fatal(err)
	}
	if err := store.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := applyInputSettlementMigration(ctx, store); err != nil {
		t.Fatalf("backfill re-entry: %v", err)
	}
	got, err := service.GetCampaign(ctx, request.CampaignID)
	if err != nil || got != want || got.AdmittedInputTokens != 4000 || !got.Blocked {
		t.Fatalf("migration changed more than settled input: got=%+v want=%+v err=%v", got, want, err)
	}
	if err = service.RecordResponse(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	afterReplay, _ := service.GetCampaign(ctx, request.CampaignID)
	if afterReplay != got {
		t.Fatalf("old receipt refunded input twice: before=%+v after=%+v", got, afterReplay)
	}
	stored, _ := service.GetRequest(ctx, request.ID)
	if stored.Request != request || stored.Receipt.Usage != receipt.Usage {
		t.Fatal("backfill rewrote original request or receipt")
	}
	if _, err = service.Reserve(ctx, admission.Request{ID: uuid.NewString(), CampaignID: request.CampaignID, AttemptID: request.AttemptID,
		Phase: request.Phase, ConfigVersion: request.ConfigVersion, PayloadHash: request.PayloadHash, Provider: request.Provider, Model: request.Model,
		External: true, InputTokenBound: 1, OutputTokenBound: 1}, pricing); !errors.Is(err, admission.ErrUncertain) {
		t.Fatalf("migration silently lifted uncertainty block: %v", err)
	}
}

func applyInputSettlementMigration(ctx context.Context, store *PostgresStore) error {
	body, err := migrationFS.ReadFile("migrations/010_ai_input_settlement.sql")
	if err != nil {
		return err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, string(body)); err != nil {
		return err
	}
	return tx.Commit()
}

func TestBudgetPostgresInputSettlementMigrationRejectsCorruptReceiptsIntegration(t *testing.T) {
	for _, scenario := range []struct{ name, corrupt string }{
		{"missing receipt", `receipt_json=NULL`},
		{"missing usage", `receipt_json=receipt_json-'usage'`},
		{"overbound input", `receipt_json=jsonb_set(receipt_json,'{usage,prompt_tokens}','6001')`},
		{"inconsistent total", `receipt_json=jsonb_set(receipt_json,'{usage,total_tokens}','1')`},
		{"mismatched identity", `receipt_json=jsonb_set(receipt_json,'{request_id}','"different-request"')`},
		{"untrusted prompt type", `receipt_json=jsonb_set(receipt_json,'{usage,prompt_tokens}','"1000"')`},
		{"negative prompt", `receipt_json=jsonb_set(receipt_json,'{usage,prompt_tokens}','-1')`},
		{"fractional prompt", `receipt_json=jsonb_set(receipt_json,'{usage,prompt_tokens}','1.5')`},
		{"missing detail", `receipt_json=receipt_json #- '{usage,cached_input_tokens}'`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			store, service, pricing, request := inputSettlementFixture(t)
			ctx := context.Background()
			settledInputReceipt(t, service, pricing, request)
			if _, err := store.db.Exec(`UPDATE ai_budget_campaigns SET admitted_input_tokens=6000 WHERE id=$1`, request.CampaignID); err != nil {
				t.Fatal(err)
			}
			before, _ := service.GetCampaign(ctx, request.CampaignID)
			if _, err := store.db.Exec(`UPDATE ai_model_requests SET `+scenario.corrupt+` WHERE id=$1`, request.ID); err != nil {
				t.Fatal(err)
			}
			if err := applyInputSettlementMigration(ctx, store); err == nil {
				t.Fatal("corrupt receipt released input allowance")
			}
			after, _ := service.GetCampaign(ctx, request.CampaignID)
			if after != before {
				t.Fatalf("failed migration changed counters or caps: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestBudgetPostgresInputSettlementMigrationRejectsCounterDriftIntegration(t *testing.T) {
	for _, accounted := range []bool{false, true} {
		t.Run(fmt.Sprintf("has-request-%v", accounted), func(t *testing.T) {
			store, service, pricing, request := inputSettlementFixture(t)
			ctx := context.Background()
			if accounted {
				settledInputReceipt(t, service, pricing, request)
			}
			if _, err := store.db.Exec(`UPDATE ai_budget_campaigns SET admitted_input_tokens=9999 WHERE id=$1`, request.CampaignID); err != nil {
				t.Fatal(err)
			}
			before, _ := service.GetCampaign(ctx, request.CampaignID)
			if err := applyInputSettlementMigration(ctx, store); err == nil {
				t.Fatal("inconsistent accounting was reset to grant allowance")
			}
			after, _ := service.GetCampaign(ctx, request.CampaignID)
			if after != before {
				t.Fatalf("failed migration changed existing accounting: before=%+v after=%+v", before, after)
			}
		})
	}
}
