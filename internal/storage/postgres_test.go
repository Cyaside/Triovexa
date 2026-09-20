package storage

import (
	"context"
	"io/fs"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Cyaside/Triovexa/internal/domain"
)

func TestEmbeddedMigrationsAreNumbered(t *testing.T) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	pattern := regexp.MustCompile(`^[0-9]{3}_[a-z0-9_]+\.sql$`)
	if len(entries) == 0 {
		t.Fatal("expected at least one migration")
	}
	for _, entry := range entries {
		if !pattern.MatchString(entry.Name()) {
			t.Fatalf("migration %q is not numbered", entry.Name())
		}
	}
}

func TestPostgresStoreCreatesIncidentAuditAndJobInOneTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sqlmock: %v", err)
	}
	defer db.Close()
	store := &PostgresStore{db: db}
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO incidents`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO audit_events`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO workflow_jobs`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	err = store.CreateIncidentIntake(context.Background(),
		domain.Incident{ID: "inc-1", State: domain.IncidentStateTriaging, CreatedAt: now, UpdatedAt: now},
		domain.AuditEvent{ID: "audit-1", IncidentID: "inc-1", DetailsJSON: `{}`, StartedAt: now, FinishedAt: now},
		domain.WorkflowJob{ID: "job-1", Type: domain.JobTypeTriage, DedupKey: "triage:inc-1", PayloadJSON: `{}`, MaxAttempts: 3, AvailableAt: now, CreatedAt: now},
	)
	if err != nil {
		t.Fatalf("create atomic intake: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

func TestMemoryStoreCompareAndSwapIncidentStateRejectsStaleState(t *testing.T) {
	store := NewMemoryStore()
	now := time.Now().UTC()
	if err := store.CreateIncident(context.Background(), domain.Incident{ID: "inc-cas", State: domain.IncidentStateTriaging, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	updated, err := store.CompareAndSwapIncidentState(context.Background(), "inc-cas", domain.IncidentStateDetected, domain.IncidentStateResolved)
	if err != nil {
		t.Fatal(err)
	}
	if updated {
		t.Fatal("stale transition unexpectedly succeeded")
	}
	incident, _ := store.GetIncident(context.Background(), "inc-cas")
	if incident.State != domain.IncidentStateTriaging {
		t.Fatalf("state = %q, want triaging", incident.State)
	}
}

func TestPostgresStoreUpdateIncidentStateReturnsNotFoundWhenNoRowsChange(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sqlmock: %v", err)
	}
	defer db.Close()

	store := &PostgresStore{db: db}
	mock.ExpectExec(`UPDATE incidents`).
		WithArgs(string(domain.IncidentStateResolved), sqlmock.AnyArg(), "incident-missing").
		WillReturnResult(sqlmock.NewResult(0, 0))

	err = store.UpdateIncidentState(context.Background(), "incident-missing", domain.IncidentStateResolved)
	if err != ErrNotFound {
		t.Fatalf("error = %v, want %v", err, ErrNotFound)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

func TestPostgresStoreUpdateIncidentStateSucceedsWhenRowExists(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sqlmock: %v", err)
	}
	defer db.Close()

	store := &PostgresStore{db: db}
	mock.ExpectExec(`UPDATE incidents`).
		WithArgs(string(domain.IncidentStateResolved), sqlmock.AnyArg(), "incident-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.UpdateIncidentState(context.Background(), "incident-1", domain.IncidentStateResolved); err != nil {
		t.Fatalf("update incident state: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}
