package postgres

import (
	"context"
	"database/sql"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/domain"
)

func TestPostgresMigrationsAndAtomicIntakeIntegration(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	store, err := NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var migrationCount int
	if err := store.db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&migrationCount); err != nil || migrationCount == 0 {
		t.Fatalf("migration count=%d err=%v", migrationCount, err)
	}
	now := time.Now().UTC()
	incidentID := uuid.NewString()
	incident := domain.Incident{ID: incidentID, ExternalAlertID: "integration-" + incidentID, AlertSource: "integration", Title: "atomic intake", ServiceName: "queue-worker", Environment: "test", Severity: "high", State: domain.IncidentStateTriaging, CreatedAt: now, UpdatedAt: now}
	event := domain.AuditEvent{ID: uuid.NewString(), IncidentID: incidentID, StepName: "webhook_intake", Status: "completed", DetailsJSON: `{}`, StartedAt: now, FinishedAt: now}
	job := domain.WorkflowJob{ID: uuid.NewString(), Type: domain.JobTypeTriage, DedupKey: "triage:" + incidentID, PayloadJSON: `{"incident_id":"` + incidentID + `"}`, Status: domain.JobQueued, MaxAttempts: 3, AvailableAt: now, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateIncidentIntake(context.Background(), incident, event, job); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetIncident(context.Background(), incidentID); err != nil {
		t.Fatalf("incident missing after atomic intake: %v", err)
	}
	audit, err := store.ListAuditEvents(context.Background(), incidentID)
	if err != nil || len(audit) != 1 {
		t.Fatalf("audit events=%d err=%v", len(audit), err)
	}
	// Query the row directly because an application worker may legitimately claim it
	// when this test runs against the live Compose database.
	var persistedID, persistedDedupKey string
	if err := store.db.QueryRow(`SELECT id, dedup_key FROM workflow_jobs WHERE id = $1`, job.ID).Scan(&persistedID, &persistedDedupKey); err != nil {
		t.Fatalf("triage job missing after atomic intake: %v", err)
	}
	if persistedID != job.ID || persistedDedupKey != job.DedupKey {
		t.Fatalf("persisted job id=%q dedup=%q", persistedID, persistedDedupKey)
	}
	if err := store.migrate(context.Background()); err != nil {
		t.Fatalf("idempotent migration upgrade failed: %v", err)
	}
}

func TestPostgresConcurrentStartupAppliesMigrationsOnceIntegration(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	admin, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := "startup_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) })
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	var wg sync.WaitGroup
	results := make(chan *PostgresStore, 2)
	errorsCh := make(chan error, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			store, err := NewPostgresStore(parsed.String())
			results <- store
			errorsCh <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatalf("concurrent startup failed: %v", err)
		}
	}
	var stores []*PostgresStore
	for store := range results {
		if store != nil {
			stores = append(stores, store)
			t.Cleanup(func() { _ = store.Close() })
		}
	}
	if len(stores) != 2 {
		t.Fatalf("concurrent startup produced %d stores", len(stores))
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	var count, uniqueCount int
	if err := stores[0].db.QueryRow(`SELECT count(*),count(DISTINCT version) FROM schema_migrations`).Scan(&count, &uniqueCount); err != nil || count != len(entries) || uniqueCount != count {
		t.Fatalf("migrations applied %d times, unique=%d want=%d err=%v", count, uniqueCount, len(entries), err)
	}
	if err := stores[1].migrate(context.Background()); err != nil {
		t.Fatalf("migration re-entry failed: %v", err)
	}
}
