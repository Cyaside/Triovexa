package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai/admission"
	"github.com/google/uuid"
)

func budgetIntegrationStore(t *testing.T) (*PostgresStore, string, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := "ai_budget_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`) })
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	scopedDSN := parsed.String()
	store, err := NewPostgresStore(scopedDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, scopedDSN, schema
}
func budgetIntegrationFixture(t *testing.T, store *PostgresStore, ceiling int64) (*admission.Service, admission.Pricing, admission.Request) {
	t.Helper()
	service, err := admission.NewService(store.ModelBudgetStore())
	if err != nil {
		t.Fatal(err)
	}
	c := admission.Campaign{ID: uuid.NewString(), Profile: "synthetic-integration", MaxSpendMicroUSD: ceiling, MaxInputTokens: 100000, MaxRequests: 100}
	if err = service.CreateCampaign(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	p := admission.Pricing{Version: "synthetic-v1", Provider: "fixture", Model: "fixture", Verified: true, InputBoundVerified: true, BillableOutputBound: true, InputMicroUSDPerMillion: 1_000_000, OutputMicroUSDPerMillion: 1_000_000}
	r := admission.Request{ID: uuid.NewString(), CampaignID: c.ID, AttemptID: uuid.NewString(), Phase: "repair", ConfigVersion: "fixture-v1", PayloadHash: admission.PayloadHash([]byte("synthetic payload")), Provider: p.Provider, Model: p.Model, External: true, InputTokenBound: 10, OutputTokenBound: 5}
	return service, p, r
}

func TestBudgetPostgresConcurrentReservationsShareCeilingIntegration(t *testing.T) {
	store, _, _ := budgetIntegrationStore(t)
	service, p, r := budgetIntegrationFixture(t, store, 30)
	var wg sync.WaitGroup
	var accepted atomic.Int64
	for n := 0; n < 16; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			candidate := r
			candidate.ID = fmt.Sprintf("%s-%d", r.ID, n)
			candidate.AttemptID = uuid.NewString()
			if n%2 == 0 {
				candidate.Phase = "triage"
			}
			_, err := service.Reserve(context.Background(), candidate, p)
			if err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, admission.ErrBudgetExceeded) {
				t.Errorf("reserve: %v", err)
			}
		}(n)
	}
	wg.Wait()
	c, err := service.GetCampaign(context.Background(), r.CampaignID)
	if err != nil || accepted.Load() != 2 || c.ReservedMicroUSD != 30 || c.Requests != 2 {
		t.Fatalf("campaign=%+v accepted=%d err=%v", c, accepted.Load(), err)
	}
}

func TestBudgetPostgresReceiptReplayAndUsagePersistIntegration(t *testing.T) {
	store, dsn, _ := budgetIntegrationStore(t)
	service, p, r := budgetIntegrationFixture(t, store, 100)
	ctx := context.Background()
	if _, err := service.Reserve(ctx, r, p); err != nil {
		t.Fatal(err)
	}
	if err := service.StartDispatch(ctx, r.ID, r.PayloadHash); err != nil {
		t.Fatal(err)
	}
	receipt := admission.Receipt{RequestID: r.ID, PayloadHash: r.PayloadHash, Usage: admission.Usage{Present: true, PromptTokens: 8, CompletionTokens: 3, TotalTokens: 11, CachedInputTokens: 3, ReasoningTokens: 1}, HTTPStatus: 200, Response: []byte("opaque-sealed-malformed-model-content"), ResponseEncoding: "sealed-v1"}
	if err := service.RecordResponse(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	after, _ := admission.NewService(restarted.ModelBudgetStore())
	replayed, err := after.Reserve(ctx, r, p)
	if err != nil || replayed.State != admission.Accounted || string(replayed.Receipt.Response) != string(receipt.Response) {
		t.Fatalf("receipt lost: %+v %v", replayed, err)
	}
	if err = after.RecordResponse(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	if err = after.StartDispatch(ctx, r.ID, r.PayloadHash); !errors.Is(err, admission.ErrAlreadyDispatched) {
		t.Fatalf("replay dispatched: %v", err)
	}
	c, _ := after.GetCampaign(ctx, r.CampaignID)
	if c.SpentMicroUSD != 11 || c.Requests != 1 || c.ReservedMicroUSD != 0 {
		t.Fatalf("bad restored accounting: %+v", c)
	}
	r.ConfigVersion = "different-version"
	if _, err = after.Reserve(ctx, r, p); !errors.Is(err, admission.ErrMismatch) {
		t.Fatalf("config changed under stable ID: %v", err)
	}
}

func TestBudgetPostgresCrashAndMissingUsageHoldReservationIntegration(t *testing.T) {
	for _, unknownUsage := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing-usage-%v", unknownUsage), func(t *testing.T) {
			store, dsn, _ := budgetIntegrationStore(t)
			service, p, r := budgetIntegrationFixture(t, store, 100)
			ctx := context.Background()
			if _, err := service.Reserve(ctx, r, p); err != nil {
				t.Fatal(err)
			}
			if err := service.StartDispatch(ctx, r.ID, r.PayloadHash); err != nil {
				t.Fatal(err)
			}
			if unknownUsage {
				if err := service.RecordResponse(ctx, admission.Receipt{RequestID: r.ID, PayloadHash: r.PayloadHash, HTTPStatus: 200, ResponseEncoding: "sealed-v1"}); !errors.Is(err, admission.ErrUsageInvalid) {
					t.Fatalf("missing usage=%v", err)
				}
			}
			restarted, err := NewPostgresStore(dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			after, _ := admission.NewService(restarted.ModelBudgetStore())
			next := r
			next.ID = uuid.NewString()
			next.AttemptID = uuid.NewString()
			next.Phase = "remediation"
			if _, err = after.Reserve(ctx, next, p); !errors.Is(err, admission.ErrUncertain) {
				t.Fatalf("restart/new run reset ambiguity: %v", err)
			}
			if err = after.CancelReserved(ctx, r.ID, r.PayloadHash); !errors.Is(err, admission.ErrAlreadyDispatched) {
				t.Fatalf("uncertain released: %v", err)
			}
			c, _ := after.GetCampaign(ctx, r.CampaignID)
			if c.ReservedMicroUSD != 15 || c.SpentMicroUSD != 0 {
				t.Fatalf("reservation disappeared: %+v", c)
			}
		})
	}
}

func TestBudgetPostgresCancelBeforeDispatchAndReceiptProtectionIntegration(t *testing.T) {
	store, _, _ := budgetIntegrationStore(t)
	service, p, r := budgetIntegrationFixture(t, store, 15)
	ctx := context.Background()
	_, _ = service.Reserve(ctx, r, p)
	if err := service.CancelReserved(ctx, r.ID, r.PayloadHash); err != nil {
		t.Fatal(err)
	}
	next := r
	next.ID = uuid.NewString()
	if _, err := service.Reserve(ctx, next, p); err != nil {
		t.Fatalf("safe undispatched cancellation did not release: %v", err)
	}
	if err := service.StartDispatch(ctx, next.ID, next.PayloadHash); err != nil {
		t.Fatal(err)
	}
	if err := service.RecordResponse(ctx, admission.Receipt{RequestID: next.ID, PayloadHash: next.PayloadHash, HTTPStatus: 200, ResponseEncoding: "raw", Response: []byte("private source"), Usage: admission.Usage{Present: true}}); !errors.Is(err, admission.ErrInvalid) {
		t.Fatalf("unsealed persistent response allowed: %v", err)
	}
}

func TestBudgetPostgresBackupRestorePreservesUncertaintyIntegration(t *testing.T) {
	if _, err := exec.LookPath("pg_dump"); err != nil {
		t.Skip("pg_dump is unavailable")
	}
	store, scopedDSN, schema := budgetIntegrationStore(t)
	service, p, r := budgetIntegrationFixture(t, store, 100)
	ctx := context.Background()
	_, _ = service.Reserve(ctx, r, p)
	if err := service.StartDispatch(ctx, r.ID, r.PayloadHash); err != nil {
		t.Fatal(err)
	}
	if err := service.MarkUncertain(ctx, r.ID, r.PayloadHash); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(scopedDSN)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Del("search_path")
	parsed.RawQuery = query.Encode()
	commandCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(commandCtx, "pg_dump", "--dbname", parsed.String(), "--schema", schema, "--no-owner", "--no-acl", "--format=plain", "--inserts")
	dump, err := command.Output()
	if err != nil {
		t.Fatalf("isolated schema backup failed: %v", err)
	}
	// Only the UUID-named test schema is replaced, never application data.
	if _, err = store.db.ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`); err != nil {
		t.Fatal(err)
	}
	// pg_dump 17+ emits psql meta-commands; they are not SQL statements.
	lines := strings.Split(string(dump), "\n")
	kept := lines[:0]
	for _, line := range lines {
		if !strings.HasPrefix(line, `\`) {
			kept = append(kept, line)
		}
	}
	if _, err = store.db.ExecContext(ctx, strings.Join(kept, "\n")); err != nil {
		t.Fatalf("restore isolated schema failed: %v", err)
	}
	restarted, err := NewPostgresStore(scopedDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	after, _ := admission.NewService(restarted.ModelBudgetStore())
	c, err := after.GetCampaign(ctx, r.CampaignID)
	if err != nil || !c.Blocked || c.ReservedMicroUSD != 15 || c.Requests != 1 {
		t.Fatalf("restore reset ledger: %+v %v", c, err)
	}
	next := r
	next.ID = uuid.NewString()
	if _, err = after.Reserve(ctx, next, p); !errors.Is(err, admission.ErrUncertain) {
		t.Fatalf("restore granted fresh allowance: %v", err)
	}
}
