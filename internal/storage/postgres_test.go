package storage

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Cyaside/Triovexa/internal/domain"
)

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
