package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Cyaside/Triovexa/internal/domain"
)

type PostgresStore struct {
	db *sql.DB
}

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

	store := &PostgresStore{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate postgres database: %w", err)
	}

	return store, nil
}

func (s *PostgresStore) Close() error {
	return s.db.Close()
}

func (s *PostgresStore) migrate(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS incidents (
			id TEXT PRIMARY KEY,
			external_alert_id TEXT NOT NULL,
			alert_source TEXT NOT NULL,
			title TEXT NOT NULL,
			service_name TEXT NOT NULL,
			environment TEXT NOT NULL,
			severity TEXT NOT NULL,
			state TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS audit_events (
			id TEXT PRIMARY KEY,
			incident_id TEXT NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
			step_name TEXT NOT NULL,
			status TEXT NOT NULL,
			details_json JSONB NOT NULL,
			started_at TIMESTAMPTZ NOT NULL,
			finished_at TIMESTAMPTZ NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS evidence_items (
			id TEXT PRIMARY KEY,
			incident_id TEXT NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
			type TEXT NOT NULL,
			source TEXT NOT NULL,
			snippet TEXT NOT NULL,
			timestamp TIMESTAMPTZ NOT NULL,
			metadata_json JSONB NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS document_references (
			id TEXT PRIMARY KEY,
			incident_id TEXT NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
			document_title TEXT NOT NULL,
			document_type TEXT NOT NULL,
			relevance_reason TEXT NOT NULL,
			snippet TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS triage_results (
			id TEXT PRIMARY KEY,
			incident_id TEXT NOT NULL UNIQUE REFERENCES incidents(id) ON DELETE CASCADE,
			summary TEXT NOT NULL,
			hypotheses_json JSONB NOT NULL,
			blast_radius TEXT NOT NULL,
			next_steps_json JSONB NOT NULL,
			draft_status_update TEXT NOT NULL,
			confidence_notes TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL
		);`,
	}

	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}

	return nil
}

func (s *PostgresStore) CreateIncident(ctx context.Context, incident domain.Incident) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO incidents (
			id, external_alert_id, alert_source, title, service_name, environment, severity, state, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`,
		incident.ID,
		incident.ExternalAlertID,
		incident.AlertSource,
		incident.Title,
		incident.ServiceName,
		incident.Environment,
		incident.Severity,
		string(incident.State),
		incident.CreatedAt.UTC(),
		incident.UpdatedAt.UTC(),
	)
	return err
}

func (s *PostgresStore) UpdateIncidentState(ctx context.Context, incidentID string, state domain.IncidentState) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE incidents
		SET state = $1, updated_at = $2
		WHERE id = $3
	`, string(state), time.Now().UTC(), incidentID)
	return err
}

func (s *PostgresStore) GetIncident(ctx context.Context, incidentID string) (domain.Incident, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, external_alert_id, alert_source, title, service_name, environment, severity, state, created_at, updated_at
		FROM incidents
		WHERE id = $1
	`, incidentID)

	var incident domain.Incident
	var state string
	if err := row.Scan(
		&incident.ID,
		&incident.ExternalAlertID,
		&incident.AlertSource,
		&incident.Title,
		&incident.ServiceName,
		&incident.Environment,
		&incident.Severity,
		&state,
		&incident.CreatedAt,
		&incident.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Incident{}, ErrNotFound
		}
		return domain.Incident{}, err
	}

	incident.State = domain.IncidentState(state)
	return incident, nil
}

func (s *PostgresStore) ListIncidents(ctx context.Context) ([]domain.Incident, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, external_alert_id, alert_source, title, service_name, environment, severity, state, created_at, updated_at
		FROM incidents
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var incidents []domain.Incident
	for rows.Next() {
		var incident domain.Incident
		var state string
		if err := rows.Scan(
			&incident.ID,
			&incident.ExternalAlertID,
			&incident.AlertSource,
			&incident.Title,
			&incident.ServiceName,
			&incident.Environment,
			&incident.Severity,
			&state,
			&incident.CreatedAt,
			&incident.UpdatedAt,
		); err != nil {
			return nil, err
		}
		incident.State = domain.IncidentState(state)
		incidents = append(incidents, incident)
	}

	return incidents, rows.Err()
}

func (s *PostgresStore) AddAuditEvent(ctx context.Context, event domain.AuditEvent) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_events (id, incident_id, step_name, status, details_json, started_at, finished_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7)
	`,
		event.ID,
		event.IncidentID,
		event.StepName,
		event.Status,
		event.DetailsJSON,
		event.StartedAt.UTC(),
		event.FinishedAt.UTC(),
	)
	return err
}

func (s *PostgresStore) ListAuditEvents(ctx context.Context, incidentID string) ([]domain.AuditEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, incident_id, step_name, status, details_json, started_at, finished_at
		FROM audit_events
		WHERE incident_id = $1
		ORDER BY started_at ASC
	`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []domain.AuditEvent
	for rows.Next() {
		var event domain.AuditEvent
		if err := rows.Scan(
			&event.ID,
			&event.IncidentID,
			&event.StepName,
			&event.Status,
			&event.DetailsJSON,
			&event.StartedAt,
			&event.FinishedAt,
		); err != nil {
			return nil, err
		}
		events = append(events, event)
	}

	return events, rows.Err()
}

func (s *PostgresStore) SaveEvidenceItems(ctx context.Context, items []domain.EvidenceItem) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	for _, item := range items {
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO evidence_items (id, incident_id, type, source, snippet, timestamp, metadata_json)
			VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)
		`, item.ID, item.IncidentID, item.Type, item.Source, item.Snippet, item.Timestamp.UTC(), item.MetadataJSON); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *PostgresStore) ListEvidenceItems(ctx context.Context, incidentID string) ([]domain.EvidenceItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, incident_id, type, source, snippet, timestamp, metadata_json
		FROM evidence_items
		WHERE incident_id = $1
		ORDER BY timestamp ASC
	`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []domain.EvidenceItem
	for rows.Next() {
		var item domain.EvidenceItem
		if err := rows.Scan(
			&item.ID,
			&item.IncidentID,
			&item.Type,
			&item.Source,
			&item.Snippet,
			&item.Timestamp,
			&item.MetadataJSON,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}

	return items, rows.Err()
}

func (s *PostgresStore) SaveDocumentReferences(ctx context.Context, references []domain.DocumentReference) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	for _, reference := range references {
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO document_references (id, incident_id, document_title, document_type, relevance_reason, snippet)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, reference.ID, reference.IncidentID, reference.DocumentTitle, reference.DocumentType, reference.RelevanceReason, reference.Snippet); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *PostgresStore) ListDocumentReferences(ctx context.Context, incidentID string) ([]domain.DocumentReference, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, incident_id, document_title, document_type, relevance_reason, snippet
		FROM document_references
		WHERE incident_id = $1
	`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var references []domain.DocumentReference
	for rows.Next() {
		var reference domain.DocumentReference
		if err := rows.Scan(
			&reference.ID,
			&reference.IncidentID,
			&reference.DocumentTitle,
			&reference.DocumentType,
			&reference.RelevanceReason,
			&reference.Snippet,
		); err != nil {
			return nil, err
		}
		references = append(references, reference)
	}

	return references, rows.Err()
}

func (s *PostgresStore) SaveTriageResult(ctx context.Context, result domain.TriageResult) error {
	hypothesesJSON, err := json.Marshal(result.Hypotheses)
	if err != nil {
		return err
	}

	nextStepsJSON, err := json.Marshal(result.NextSteps)
	if err != nil {
		return err
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO triage_results (id, incident_id, summary, hypotheses_json, blast_radius, next_steps_json, draft_status_update, confidence_notes, created_at)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6::jsonb, $7, $8, $9)
		ON CONFLICT(incident_id) DO UPDATE SET
			summary = excluded.summary,
			hypotheses_json = excluded.hypotheses_json,
			blast_radius = excluded.blast_radius,
			next_steps_json = excluded.next_steps_json,
			draft_status_update = excluded.draft_status_update,
			confidence_notes = excluded.confidence_notes,
			created_at = excluded.created_at
	`,
		result.ID,
		result.IncidentID,
		result.Summary,
		string(hypothesesJSON),
		result.BlastRadius,
		string(nextStepsJSON),
		result.DraftStatusUpdate,
		result.ConfidenceNotes,
		result.CreatedAt.UTC(),
	)
	return err
}

func (s *PostgresStore) GetTriageResult(ctx context.Context, incidentID string) (domain.TriageResult, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, incident_id, summary, hypotheses_json, blast_radius, next_steps_json, draft_status_update, confidence_notes, created_at
		FROM triage_results
		WHERE incident_id = $1
	`, incidentID)

	var result domain.TriageResult
	var hypothesesJSON string
	var nextStepsJSON string
	if err := row.Scan(
		&result.ID,
		&result.IncidentID,
		&result.Summary,
		&hypothesesJSON,
		&result.BlastRadius,
		&nextStepsJSON,
		&result.DraftStatusUpdate,
		&result.ConfidenceNotes,
		&result.CreatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.TriageResult{}, ErrNotFound
		}
		return domain.TriageResult{}, err
	}

	if err := json.Unmarshal([]byte(hypothesesJSON), &result.Hypotheses); err != nil {
		return domain.TriageResult{}, err
	}
	if err := json.Unmarshal([]byte(nextStepsJSON), &result.NextSteps); err != nil {
		return domain.TriageResult{}, err
	}
	return result, nil
}
