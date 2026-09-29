package postgres

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	repairverify "github.com/Cyaside/Triovexa/internal/coderepair/verification"
	"github.com/Cyaside/Triovexa/internal/storage"
	"github.com/Cyaside/Triovexa/internal/storage/postgres/repair"
)

var ErrNotFound = storage.ErrNotFound
var ErrNoJobAvailable = storage.ErrNoJobAvailable

type PostgresStore struct {
	db *sql.DB
	*repair.Store
}

var _ storage.Repository = (*PostgresStore)(nil)
var _ coderepair.ProposalRepository = (*PostgresStore)(nil)
var _ coderepair.InvestigationApprovalRepository = (*PostgresStore)(nil)

type RepairDeployment = repairverify.Deployment
type RepairPREvent = coderepair.PREvent

//go:embed migrations/*.sql
var migrationFS embed.FS

func NewPostgresStore(databaseURL string) (*PostgresStore, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open postgres database: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres database: %w", err)
	}

	store := &PostgresStore{db: db, Store: repair.New(db)}
	migrationCtx, migrationCancel := context.WithTimeout(context.Background(), time.Minute)
	defer migrationCancel()
	if err := store.migrate(migrationCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate postgres database: %w", err)
	}

	return store, nil
}

func (s *PostgresStore) Close() error {
	return s.db.Close()
}

func (s *PostgresStore) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *PostgresStore) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migrations: %w", err)
	}
	defer tx.Rollback()
	// Keep the initial table creation and version checks behind the same lock.
	// Concurrent server startups then observe a fully committed migration set.
	var lock any
	if err := tx.QueryRowContext(ctx, `SELECT pg_advisory_xact_lock(1433770068, hashtext(current_schema()))`).Scan(&lock); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version := strings.TrimSuffix(entry.Name(), ".sql")
		var applied bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", version, err)
		}
		if applied {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", version, err)
		}
		if _, err = tx.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("apply migration %s: %w", version, err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			return fmt.Errorf("record migration %s: %w", version, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}
