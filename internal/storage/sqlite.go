package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Cyaside/Triovexa/internal/domain"
)

var ErrNotFound = errors.New("storage: not found")

type Repository interface {
	Close() error
	CreateIncident(context.Context, domain.Incident) error
	UpdateIncidentState(context.Context, string, domain.IncidentState) error
	GetIncident(context.Context, string) (domain.Incident, error)
	ListIncidents(context.Context) ([]domain.Incident, error)
	AddAuditEvent(context.Context, domain.AuditEvent) error
	ListAuditEvents(context.Context, string) ([]domain.AuditEvent, error)
	SaveEvidenceItems(context.Context, []domain.EvidenceItem) error
	ListEvidenceItems(context.Context, string) ([]domain.EvidenceItem, error)
	SaveDocumentReferences(context.Context, []domain.DocumentReference) error
	ListDocumentReferences(context.Context, string) ([]domain.DocumentReference, error)
	SaveTriageResult(context.Context, domain.TriageResult) error
	GetTriageResult(context.Context, string) (domain.TriageResult, error)
}

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(databasePath string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}

	store := &SQLiteStore{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate sqlite database: %w", err)
	}

	return store, nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

func (s *SQLiteStore) migrate(ctx context.Context) error {
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
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS audit_events (
			id TEXT PRIMARY KEY,
			incident_id TEXT NOT NULL,
			step_name TEXT NOT NULL,
			status TEXT NOT NULL,
			details_json TEXT NOT NULL,
			started_at TEXT NOT NULL,
			finished_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS evidence_items (
			id TEXT PRIMARY KEY,
			incident_id TEXT NOT NULL,
			type TEXT NOT NULL,
			source TEXT NOT NULL,
			snippet TEXT NOT NULL,
			timestamp TEXT NOT NULL,
			metadata_json TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS document_references (
			id TEXT PRIMARY KEY,
			incident_id TEXT NOT NULL,
			document_title TEXT NOT NULL,
			document_type TEXT NOT NULL,
			relevance_reason TEXT NOT NULL,
			snippet TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS triage_results (
			id TEXT PRIMARY KEY,
			incident_id TEXT NOT NULL UNIQUE,
			summary TEXT NOT NULL,
			hypotheses_json TEXT NOT NULL,
			blast_radius TEXT NOT NULL,
			next_steps_json TEXT NOT NULL,
			draft_status_update TEXT NOT NULL,
			confidence_notes TEXT NOT NULL,
			created_at TEXT NOT NULL
		);`,
	}

	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}

	return nil
}

func (s *SQLiteStore) CreateIncident(ctx context.Context, incident domain.Incident) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO incidents (
			id, external_alert_id, alert_source, title, service_name, environment, severity, state, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		incident.ID,
		incident.ExternalAlertID,
		incident.AlertSource,
		incident.Title,
		incident.ServiceName,
		incident.Environment,
		incident.Severity,
		string(incident.State),
		formatTime(incident.CreatedAt),
		formatTime(incident.UpdatedAt),
	)
	return err
}

func (s *SQLiteStore) UpdateIncidentState(ctx context.Context, incidentID string, state domain.IncidentState) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE incidents
		SET state = ?, updated_at = ?
		WHERE id = ?
	`, string(state), formatTime(time.Now().UTC()), incidentID)
	return err
}

func (s *SQLiteStore) GetIncident(ctx context.Context, incidentID string) (domain.Incident, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, external_alert_id, alert_source, title, service_name, environment, severity, state, created_at, updated_at
		FROM incidents
		WHERE id = ?
	`, incidentID)

	var incident domain.Incident
	var state string
	var createdAt string
	var updatedAt string
	if err := row.Scan(
		&incident.ID,
		&incident.ExternalAlertID,
		&incident.AlertSource,
		&incident.Title,
		&incident.ServiceName,
		&incident.Environment,
		&incident.Severity,
		&state,
		&createdAt,
		&updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Incident{}, ErrNotFound
		}
		return domain.Incident{}, err
	}

	incident.State = domain.IncidentState(state)
	incident.CreatedAt = parseTime(createdAt)
	incident.UpdatedAt = parseTime(updatedAt)
	return incident, nil
}

func (s *SQLiteStore) ListIncidents(ctx context.Context) ([]domain.Incident, error) {
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
		var createdAt string
		var updatedAt string
		if err := rows.Scan(
			&incident.ID,
			&incident.ExternalAlertID,
			&incident.AlertSource,
			&incident.Title,
			&incident.ServiceName,
			&incident.Environment,
			&incident.Severity,
			&state,
			&createdAt,
			&updatedAt,
		); err != nil {
			return nil, err
		}

		incident.State = domain.IncidentState(state)
		incident.CreatedAt = parseTime(createdAt)
		incident.UpdatedAt = parseTime(updatedAt)
		incidents = append(incidents, incident)
	}

	return incidents, rows.Err()
}

func (s *SQLiteStore) AddAuditEvent(ctx context.Context, event domain.AuditEvent) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_events (id, incident_id, step_name, status, details_json, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`,
		event.ID,
		event.IncidentID,
		event.StepName,
		event.Status,
		event.DetailsJSON,
		formatTime(event.StartedAt),
		formatTime(event.FinishedAt),
	)
	return err
}

func (s *SQLiteStore) ListAuditEvents(ctx context.Context, incidentID string) ([]domain.AuditEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, incident_id, step_name, status, details_json, started_at, finished_at
		FROM audit_events
		WHERE incident_id = ?
		ORDER BY started_at ASC
	`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []domain.AuditEvent
	for rows.Next() {
		var event domain.AuditEvent
		var startedAt string
		var finishedAt string
		if err := rows.Scan(
			&event.ID,
			&event.IncidentID,
			&event.StepName,
			&event.Status,
			&event.DetailsJSON,
			&startedAt,
			&finishedAt,
		); err != nil {
			return nil, err
		}

		event.StartedAt = parseTime(startedAt)
		event.FinishedAt = parseTime(finishedAt)
		events = append(events, event)
	}

	return events, rows.Err()
}

func (s *SQLiteStore) SaveEvidenceItems(ctx context.Context, items []domain.EvidenceItem) error {
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
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, item.ID, item.IncidentID, item.Type, item.Source, item.Snippet, formatTime(item.Timestamp), item.MetadataJSON); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *SQLiteStore) ListEvidenceItems(ctx context.Context, incidentID string) ([]domain.EvidenceItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, incident_id, type, source, snippet, timestamp, metadata_json
		FROM evidence_items
		WHERE incident_id = ?
		ORDER BY timestamp ASC
	`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []domain.EvidenceItem
	for rows.Next() {
		var item domain.EvidenceItem
		var timestamp string
		if err := rows.Scan(
			&item.ID,
			&item.IncidentID,
			&item.Type,
			&item.Source,
			&item.Snippet,
			&timestamp,
			&item.MetadataJSON,
		); err != nil {
			return nil, err
		}

		item.Timestamp = parseTime(timestamp)
		items = append(items, item)
	}

	return items, rows.Err()
}

func (s *SQLiteStore) SaveDocumentReferences(ctx context.Context, references []domain.DocumentReference) error {
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
			VALUES (?, ?, ?, ?, ?, ?)
		`, reference.ID, reference.IncidentID, reference.DocumentTitle, reference.DocumentType, reference.RelevanceReason, reference.Snippet); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *SQLiteStore) ListDocumentReferences(ctx context.Context, incidentID string) ([]domain.DocumentReference, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, incident_id, document_title, document_type, relevance_reason, snippet
		FROM document_references
		WHERE incident_id = ?
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

func (s *SQLiteStore) SaveTriageResult(ctx context.Context, result domain.TriageResult) error {
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
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
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
		formatTime(result.CreatedAt),
	)
	return err
}

func (s *SQLiteStore) GetTriageResult(ctx context.Context, incidentID string) (domain.TriageResult, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, incident_id, summary, hypotheses_json, blast_radius, next_steps_json, draft_status_update, confidence_notes, created_at
		FROM triage_results
		WHERE incident_id = ?
	`, incidentID)

	var result domain.TriageResult
	var hypothesesJSON string
	var nextStepsJSON string
	var createdAt string
	if err := row.Scan(
		&result.ID,
		&result.IncidentID,
		&result.Summary,
		&hypothesesJSON,
		&result.BlastRadius,
		&nextStepsJSON,
		&result.DraftStatusUpdate,
		&result.ConfidenceNotes,
		&createdAt,
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

	result.CreatedAt = parseTime(createdAt)
	return result, nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}

	return parsed
}
