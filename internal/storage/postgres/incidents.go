package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Cyaside/Triovexa/internal/domain"
)

func (s *PostgresStore) EnqueueJob(ctx context.Context, job domain.WorkflowJob) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO workflow_jobs (id, type, dedup_key, payload_json, status, attempts, max_attempts, available_at, lease_owner, lease_until, last_error, created_at, updated_at)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8, '', NULL, '', $9, $9)
		ON CONFLICT(dedup_key) DO NOTHING
	`, job.ID, job.Type, job.DedupKey, job.PayloadJSON, domain.JobQueued, job.Attempts, job.MaxAttempts, job.AvailableAt.UTC(), job.CreatedAt.UTC())
	return err
}

func (s *PostgresStore) CreateIncidentIntake(ctx context.Context, incident domain.Incident, event domain.AuditEvent, job domain.WorkflowJob) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO incidents (id, external_alert_id, alert_source, title, service_name, environment, severity, state, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, incident.ID, incident.ExternalAlertID, incident.AlertSource, incident.Title, incident.ServiceName, incident.Environment,
		incident.Severity, string(incident.State), incident.CreatedAt.UTC(), incident.UpdatedAt.UTC()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_events (id, incident_id, step_name, status, details_json, started_at, finished_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7)
	`, event.ID, event.IncidentID, event.StepName, event.Status, event.DetailsJSON, event.StartedAt.UTC(), event.FinishedAt.UTC()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO workflow_jobs (id, type, dedup_key, payload_json, status, attempts, max_attempts, available_at, lease_owner, lease_until, last_error, created_at, updated_at)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8, '', NULL, '', $9, $9)
	`, job.ID, job.Type, job.DedupKey, job.PayloadJSON, domain.JobQueued, job.Attempts, job.MaxAttempts, job.AvailableAt.UTC(), job.CreatedAt.UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PostgresStore) ClaimJob(ctx context.Context, workerID string, leaseUntil time.Time) (domain.WorkflowJob, error) {
	row := s.db.QueryRowContext(ctx, `
		WITH candidate AS (
			SELECT id FROM workflow_jobs
			WHERE attempts < max_attempts
			  AND available_at <= now()
			  AND (status = 'queued' OR (status = 'running' AND lease_until < now()))
			ORDER BY available_at, created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE workflow_jobs j
		SET status = 'running', attempts = attempts + 1, lease_owner = $1, lease_until = $2, updated_at = now()
		FROM candidate
		WHERE j.id = candidate.id
		RETURNING j.id, j.type, j.dedup_key, j.payload_json::text, j.status, j.attempts, j.max_attempts,
		          j.available_at, j.lease_owner, j.lease_until, j.last_error, j.created_at, j.updated_at
	`, workerID, leaseUntil.UTC())
	var job domain.WorkflowJob
	if err := row.Scan(&job.ID, &job.Type, &job.DedupKey, &job.PayloadJSON, &job.Status, &job.Attempts, &job.MaxAttempts,
		&job.AvailableAt, &job.LeaseOwner, &job.LeaseUntil, &job.LastError, &job.CreatedAt, &job.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.WorkflowJob{}, ErrNoJobAvailable
		}
		return domain.WorkflowJob{}, err
	}
	return job, nil
}

func (s *PostgresStore) CompleteJob(ctx context.Context, jobID string, workerID string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE workflow_jobs SET status = 'succeeded', lease_owner = '', lease_until = NULL, updated_at = now()
		WHERE id = $1 AND status = 'running' AND lease_owner = $2
	`, jobID, workerID)
	if err != nil {
		return err
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows == 0 {
		if rowsErr != nil {
			return rowsErr
		}
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) FailJob(ctx context.Context, jobID string, workerID string, message string, retryAt time.Time, terminal bool) error {
	status := domain.JobQueued
	if terminal {
		status = domain.JobDeadLetter
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE workflow_jobs
		SET status = $1, available_at = $2, lease_owner = '', lease_until = NULL, last_error = $3, updated_at = now()
		WHERE id = $4 AND status = 'running' AND lease_owner = $5
	`, status, retryAt.UTC(), message, jobID, workerID)
	if err != nil {
		return err
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows == 0 {
		if rowsErr != nil {
			return rowsErr
		}
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) RecoverTriageJobs(ctx context.Context) (int, error) {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO workflow_jobs (id, type, dedup_key, payload_json, status, attempts, max_attempts, available_at, lease_owner, last_error, created_at, updated_at)
		SELECT gen_random_uuid()::text, 'triage', 'triage:' || i.id,
		       jsonb_build_object('incident_id', i.id), 'queued', 0, 3, now(), '', '', now(), now()
		FROM incidents i
		WHERE i.state = 'triaging'
		ON CONFLICT(dedup_key) DO UPDATE
		SET status = 'queued', attempts = 0, available_at = now(), lease_owner = '', lease_until = NULL,
		    last_error = '', updated_at = now()
		WHERE workflow_jobs.status = 'dead_letter'
	`)
	if err != nil {
		return 0, err
	}
	rows, err := result.RowsAffected()
	return int(rows), err
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
	result, err := s.db.ExecContext(ctx, `
		UPDATE incidents
		SET state = $1, updated_at = $2
		WHERE id = $3
	`, string(state), time.Now().UTC(), incidentID)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return ErrNotFound
	}

	return nil
}

func (s *PostgresStore) CompareAndSwapIncidentState(ctx context.Context, incidentID string, expected, next domain.IncidentState) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE incidents SET state = $1, updated_at = $2
		WHERE id = $3 AND state = $4
	`, string(next), time.Now().UTC(), incidentID, string(expected))
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows == 1, nil
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

func (s *PostgresStore) GetLatestIncidentByExternalAlertID(ctx context.Context, externalAlertID string) (domain.Incident, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, external_alert_id, alert_source, title, service_name, environment, severity, state, created_at, updated_at
		FROM incidents
		WHERE external_alert_id = $1
		ORDER BY created_at DESC
		LIMIT 1
	`, externalAlertID)

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
