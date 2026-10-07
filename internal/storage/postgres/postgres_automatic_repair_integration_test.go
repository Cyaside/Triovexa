package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/google/uuid"
)

func seedAutomaticAlert(t *testing.T, store *PostgresStore, b coderepair.RepositoryBinding) domain.Incident {
	t.Helper()
	now := time.Now().UTC()
	i := domain.Incident{ID: uuid.NewString(), ExternalAlertID: uuid.NewString(), AlertSource: "prometheus", Title: "parser rejects supported jobs", ServiceName: b.ServiceName, Environment: b.Environment, Severity: "high", State: domain.IncidentStateTriaging, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateIncident(context.Background(), i); err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(map[string]any{"target": b.ServiceName, "complete": true, "deployed_revision": strings.Repeat("a", 40)})
	if err := store.SaveEvidenceItems(context.Background(), []domain.EvidenceItem{
		{ID: uuid.NewString(), IncidentID: i.ID, Type: "metric", Source: "workload-control", Timestamp: now, Snippet: "backlog rising", MetadataJSON: string(metadata)},
		{ID: uuid.NewString(), IncidentID: i.ID, Type: "log", Source: "grafana-loki", Timestamp: now, Snippet: "parser rejected job", MetadataJSON: `{}`},
	}); err != nil {
		t.Fatal(err)
	}
	return i
}

func automaticFixture(t *testing.T, store *PostgresStore) (*coderepair.AutomaticDispatcher, coderepair.RepositoryBinding) {
	t.Helper()
	now := time.Now().UTC()
	if err := store.CreateUser(context.Background(), domain.User{ID: "auto-admin", Username: "auto-admin", PasswordHash: "unused-fixture", Role: domain.RoleAdmin, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	b := coderepair.RepositoryBinding{ID: uuid.NewString(), ServiceName: "python-worker", Environment: "staging", RepositoryURL: "https://github.com/owner/python-worker", BaseRef: "main", AllowedPaths: []string{"src"}, TestRecipes: []string{"python-tests"}, PolicyVersion: "repair-v2", Enabled: true, CreatedAt: now, UpdatedAt: now,
		CredentialRef: "env:TEST_REPAIR_READ_TOKEN", ValidationProfile: &coderepair.ValidationProfile{ID: "python-tests", Version: "python-v1", Image: "triovexa-repair-sandbox:python", RootFiles: []string{"pyproject.toml"}, ProtectedPaths: []string{"tests"}, Test: coderepair.ValidationCommand{Executable: "/usr/local/bin/python", Arguments: []string{"-m", "unittest", "discover", "-s", "tests", "-v"}, TimeoutSeconds: 30}, ExpectedTestName: "test_accepts_one", ExpectedFailure: "one rejected"},
		Automation: &coderepair.AutomationPolicy{Enabled: true, AuthorizedBy: "auto-admin", ExpiresAt: now.Add(10 * time.Minute), CampaignID: "auto-offline", MaxInvestigations: 1, MaxModelRequests: 2}}
	if err := store.CreateRepositoryBinding(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	d := &coderepair.AutomaticDispatcher{Store: store, ResolveBase: func(context.Context, coderepair.RepositoryBinding) (string, error) {
		return strings.Repeat("b", 40), nil
	}, Allowed: func(context.Context) error { return nil }, Selection: func() (coderepair.AgentSelection, error) {
		return coderepair.AgentSelection{Provider: "openai-compatible", Model: "fixture-model", PromptVersion: "repair-native-v5", Runtime: &coderepair.RuntimeSpec{EngineID: "deepagents", EngineVersion: "1.14.1", ContractVersion: "1", CheckpointVersion: "1", Profile: "offline-fixture", ConfigVersion: "fixture-v1", CampaignID: "auto-offline", PlaybookDigest: strings.Repeat("a", 64), SnapshotSHA256: strings.Repeat("b", 64), SealedSnapshot: "v1.offline-fixture"}}, nil
	}}
	return d, b
}

func TestAutomaticRepairConcurrentAlertAndRestartIntegration(t *testing.T) {
	store := isolatedRepairStore(t)
	d, b := automaticFixture(t, store)
	i := seedAutomaticAlert(t, store, b)
	var wg sync.WaitGroup
	var lookups atomic.Int32
	d.ResolveBase = func(context.Context, coderepair.RepositoryBinding) (string, error) {
		lookups.Add(1)
		return strings.Repeat("b", 40), nil
	}
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if handled, err := d.Try(context.Background(), i); !handled || err != nil {
				t.Errorf("dispatch: %v %v", handled, err)
			}
		}()
	}
	wg.Wait()
	for _, table := range []string{"repair_cases", "repair_evidence", "repair_approvals", "repair_attempts", "repair_jobs", "repair_automatic_dispatches"} {
		var count int
		if err := store.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	var caseID string
	if err := store.db.QueryRow(`SELECT id FROM repair_cases WHERE state='investigating' AND version=2`).Scan(&caseID); err != nil {
		t.Fatal(err)
	}
	var pinned int
	if err := store.db.QueryRow(`SELECT (runtime_json->>'max_model_requests')::int FROM repair_attempts`).Scan(&pinned); err != nil || pinned != 2 {
		t.Fatalf("request cap: %d %v", pinned, err)
	}
	before := lookups.Load()
	d.Store = reopenRepairStore(t, store)
	if handled, err := d.Try(context.Background(), i); !handled || err != nil || lookups.Load() != before {
		t.Fatalf("restart lost dedup: %v %v", handled, err)
	}
	if err := store.CheckRepairInvestigationSafety(context.Background(), caseID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if handled, err := d.Try(context.Background(), seedAutomaticAlert(t, store, b)); !handled || err == nil || !strings.Contains(err.Error(), "quota") {
		t.Fatalf("quota: %v %v", handled, err)
	}
	if _, err := store.DisableRepositoryBinding(context.Background(), b.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckRepairInvestigationSafety(context.Background(), caseID, time.Now()); err == nil {
		t.Fatal("revoked binding still authorizes work")
	}
}

func TestAutomaticRepairRollbackAndSafetyIntegration(t *testing.T) {
	store := isolatedRepairStore(t)
	d, b := automaticFixture(t, store)
	i := seedAutomaticAlert(t, store, b)
	if _, err := store.db.Exec(`ALTER TABLE repair_jobs ADD CONSTRAINT fixture_reject_job CHECK(false)`); err != nil {
		t.Fatal(err)
	}
	if handled, err := d.Try(context.Background(), i); !handled || err == nil {
		t.Fatal("failed job insert was accepted")
	}
	for _, table := range []string{"repair_cases", "repair_evidence", "repair_events", "repair_approvals", "repair_attempts", "repair_automatic_dispatches"} {
		var count int
		if err := store.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s survived rollback: %d %v", table, count, err)
		}
	}
	if _, err := store.db.Exec(`ALTER TABLE repair_jobs DROP CONSTRAINT fixture_reject_job`); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSetting(context.Background(), "safety.kill_switch", "true"); err != nil {
		t.Fatal(err)
	}
	if handled, err := d.Try(context.Background(), i); !handled || err == nil {
		t.Fatal("kill switch ignored")
	}
	if err := store.PutSetting(context.Background(), "safety.kill_switch", "false"); err != nil {
		t.Fatal(err)
	}
	selection := d.Selection
	d.Selection = func() (coderepair.AgentSelection, error) {
		s, err := selection()
		s.Runtime.Profile = "final-smoke"
		return s, err
	}
	if handled, err := d.Try(context.Background(), i); !handled || err == nil {
		t.Fatal("automatic alert consumed paid validation slots")
	}
	d.Selection = selection
	if _, err := store.db.Exec(`UPDATE evidence_items SET timestamp=$1 WHERE incident_id=$2 AND type='log'`, time.Now().Add(-2*time.Minute), i.ID); err != nil {
		t.Fatal(err)
	}
	if handled, err := d.Try(context.Background(), i); !handled || err == nil {
		t.Fatal("stale log accepted")
	}
}
