package storage

import (
	"context"
	"os"
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
