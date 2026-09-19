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
		`CREATE TABLE IF NOT EXISTS candidate_actions (
			id TEXT PRIMARY KEY,
			incident_id TEXT NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
			action_type TEXT NOT NULL,
			target_resource TEXT NOT NULL,
			parameters_json JSONB NOT NULL,
			risk_level TEXT NOT NULL,
			rationale TEXT NOT NULL,
			evidence_refs_json JSONB NOT NULL,
			approval_hint TEXT NOT NULL,
			status TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS policy_decisions (
			id TEXT PRIMARY KEY,
			candidate_action_id TEXT NOT NULL UNIQUE REFERENCES candidate_actions(id) ON DELETE CASCADE,
			decision TEXT NOT NULL,
			reason TEXT NOT NULL,
			approval_required BOOLEAN NOT NULL,
			policy_rule_ref TEXT NOT NULL,
			decided_at TIMESTAMPTZ NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS approval_records (
			id TEXT PRIMARY KEY,
			candidate_action_id TEXT NOT NULL REFERENCES candidate_actions(id) ON DELETE CASCADE,
			approved_by TEXT NOT NULL,
			decision TEXT NOT NULL,
			note TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL
		);`,
		`ALTER TABLE approval_records ADD COLUMN IF NOT EXISTS action_digest TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE approval_records ADD COLUMN IF NOT EXISTS policy_version TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE approval_records ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;`,
		`CREATE TABLE IF NOT EXISTS execution_records (
			id TEXT PRIMARY KEY,
			candidate_action_id TEXT NOT NULL REFERENCES candidate_actions(id) ON DELETE CASCADE,
			idempotency_key TEXT NOT NULL,
			initiated_by TEXT NOT NULL,
			executor_type TEXT NOT NULL,
			status TEXT NOT NULL,
			started_at TIMESTAMPTZ NOT NULL,
			finished_at TIMESTAMPTZ NOT NULL,
			result_json JSONB NOT NULL
		);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS execution_records_candidate_action_unique ON execution_records(candidate_action_id);`,
		`CREATE INDEX IF NOT EXISTS incidents_external_alert_lookup ON incidents(alert_source, external_alert_id, created_at DESC);`,
		`CREATE INDEX IF NOT EXISTS audit_events_incident_lookup ON audit_events(incident_id, started_at);`,
		`CREATE INDEX IF NOT EXISTS candidate_actions_incident_lookup ON candidate_actions(incident_id, created_at);`,
		`CREATE TABLE IF NOT EXISTS workflow_jobs (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			dedup_key TEXT NOT NULL UNIQUE,
			payload_json JSONB NOT NULL,
			status TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			max_attempts INTEGER NOT NULL DEFAULT 3,
			available_at TIMESTAMPTZ NOT NULL,
			lease_owner TEXT NOT NULL DEFAULT '',
			lease_until TIMESTAMPTZ,
			last_error TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS workflow_jobs_claim_lookup ON workflow_jobs(status, available_at, lease_until);`,
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS sessions (
			token_hash TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			csrf_hash TEXT NOT NULL,
			expires_at TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS sessions_expiry_lookup ON sessions(expires_at);`,
		`CREATE TABLE IF NOT EXISTS verification_results (
			id TEXT PRIMARY KEY,
			execution_record_id TEXT NOT NULL UNIQUE REFERENCES execution_records(id) ON DELETE CASCADE,
			status TEXT NOT NULL,
			evidence_json JSONB NOT NULL,
			notes TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS rollback_records (
			id TEXT PRIMARY KEY,
			candidate_action_id TEXT NOT NULL REFERENCES candidate_actions(id) ON DELETE CASCADE,
			rollback_action_key TEXT NOT NULL,
			triggered_by TEXT NOT NULL,
			status TEXT NOT NULL,
			started_at TIMESTAMPTZ NOT NULL,
			finished_at TIMESTAMPTZ NOT NULL,
			result_json JSONB NOT NULL,
			note TEXT NOT NULL
		);`,
	}

	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}

	return nil
}

func (s *PostgresStore) CreateUser(ctx context.Context, user domain.User) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO users (id, username, password_hash, role, created_at) VALUES ($1, $2, $3, $4, $5)`,
		user.ID, user.Username, user.PasswordHash, user.Role, user.CreatedAt.UTC())
	return err
}

func (s *PostgresStore) GetUserByUsername(ctx context.Context, username string) (domain.User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx, `SELECT id, username, password_hash, role, created_at FROM users WHERE lower(username) = lower($1)`, username))
}

func (s *PostgresStore) GetUser(ctx context.Context, id string) (domain.User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx, `SELECT id, username, password_hash, role, created_at FROM users WHERE id = $1`, id))
}

func (s *PostgresStore) scanUser(row *sql.Row) (domain.User, error) {
	var user domain.User
	if err := row.Scan(&user.ID, &user.Username, &user.PasswordHash, &user.Role, &user.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.User{}, ErrNotFound
		}
		return domain.User{}, err
	}
	return user, nil
}

func (s *PostgresStore) CreateSession(ctx context.Context, session domain.Session) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (token_hash, user_id, csrf_hash, expires_at, created_at) VALUES ($1, $2, $3, $4, $5)`,
		session.TokenHash, session.UserID, session.CSRFHash, session.ExpiresAt.UTC(), session.CreatedAt.UTC())
	return err
}

func (s *PostgresStore) GetSession(ctx context.Context, tokenHash string) (domain.Session, error) {
	var session domain.Session
	err := s.db.QueryRowContext(ctx, `SELECT token_hash, user_id, csrf_hash, expires_at, created_at FROM sessions WHERE token_hash = $1 AND expires_at > now()`, tokenHash).
		Scan(&session.TokenHash, &session.UserID, &session.CSRFHash, &session.ExpiresAt, &session.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Session{}, ErrNotFound
	}
	return session, err
}

func (s *PostgresStore) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

func (s *PostgresStore) EnqueueJob(ctx context.Context, job domain.WorkflowJob) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO workflow_jobs (id, type, dedup_key, payload_json, status, attempts, max_attempts, available_at, lease_owner, lease_until, last_error, created_at, updated_at)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8, '', NULL, '', $9, $9)
		ON CONFLICT(dedup_key) DO NOTHING
	`, job.ID, job.Type, job.DedupKey, job.PayloadJSON, domain.JobQueued, job.Attempts, job.MaxAttempts, job.AvailableAt.UTC(), job.CreatedAt.UTC())
	return err
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
		ON CONFLICT(dedup_key) DO NOTHING
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

func (s *PostgresStore) SaveCandidateActions(ctx context.Context, actions []domain.CandidateAction) error {
	if len(actions) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	for _, action := range actions {
		evidenceRefsJSON, marshalErr := json.Marshal(action.EvidenceRefs)
		if marshalErr != nil {
			err = marshalErr
			return err
		}

		if _, err = tx.ExecContext(ctx, `
			INSERT INTO candidate_actions (
				id, incident_id, action_type, target_resource, parameters_json, risk_level, rationale, evidence_refs_json, approval_hint, status, created_at
			) VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8::jsonb, $9, $10, $11)
			ON CONFLICT(id) DO UPDATE SET
				action_type = excluded.action_type,
				target_resource = excluded.target_resource,
				parameters_json = excluded.parameters_json,
				risk_level = excluded.risk_level,
				rationale = excluded.rationale,
				evidence_refs_json = excluded.evidence_refs_json,
				approval_hint = excluded.approval_hint,
				status = excluded.status,
				created_at = excluded.created_at
		`,
			action.ID,
			action.IncidentID,
			action.ActionType,
			action.TargetResource,
			action.ParametersJSON,
			string(action.RiskLevel),
			action.Rationale,
			string(evidenceRefsJSON),
			action.ApprovalHint,
			string(action.Status),
			action.CreatedAt.UTC(),
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *PostgresStore) ListCandidateActions(ctx context.Context, incidentID string) ([]domain.CandidateAction, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, incident_id, action_type, target_resource, parameters_json, risk_level, rationale, evidence_refs_json, approval_hint, status, created_at
		FROM candidate_actions
		WHERE incident_id = $1
		ORDER BY created_at ASC
	`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var actions []domain.CandidateAction
	for rows.Next() {
		var action domain.CandidateAction
		var riskLevel string
		var evidenceRefsJSON string
		var status string
		if err := rows.Scan(
			&action.ID,
			&action.IncidentID,
			&action.ActionType,
			&action.TargetResource,
			&action.ParametersJSON,
			&riskLevel,
			&action.Rationale,
			&evidenceRefsJSON,
			&action.ApprovalHint,
			&status,
			&action.CreatedAt,
		); err != nil {
			return nil, err
		}

		action.RiskLevel = domain.RiskLevel(riskLevel)
		action.Status = domain.CandidateActionStatus(status)
		if err := json.Unmarshal([]byte(evidenceRefsJSON), &action.EvidenceRefs); err != nil {
			return nil, err
		}

		actions = append(actions, action)
	}

	return actions, rows.Err()
}

func (s *PostgresStore) GetCandidateAction(ctx context.Context, actionID string) (domain.CandidateAction, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, incident_id, action_type, target_resource, parameters_json, risk_level, rationale, evidence_refs_json, approval_hint, status, created_at
		FROM candidate_actions
		WHERE id = $1
	`, actionID)

	var action domain.CandidateAction
	var riskLevel string
	var evidenceRefsJSON string
	var status string
	if err := row.Scan(
		&action.ID,
		&action.IncidentID,
		&action.ActionType,
		&action.TargetResource,
		&action.ParametersJSON,
		&riskLevel,
		&action.Rationale,
		&evidenceRefsJSON,
		&action.ApprovalHint,
		&status,
		&action.CreatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.CandidateAction{}, ErrNotFound
		}
		return domain.CandidateAction{}, err
	}

	action.RiskLevel = domain.RiskLevel(riskLevel)
	action.Status = domain.CandidateActionStatus(status)
	if err := json.Unmarshal([]byte(evidenceRefsJSON), &action.EvidenceRefs); err != nil {
		return domain.CandidateAction{}, err
	}

	return action, nil
}

func (s *PostgresStore) UpdateCandidateActionStatus(ctx context.Context, actionID string, status domain.CandidateActionStatus) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE candidate_actions
		SET status = $1
		WHERE id = $2
	`, string(status), actionID)
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

func (s *PostgresStore) SavePolicyDecisions(ctx context.Context, decisions []domain.PolicyDecision) error {
	if len(decisions) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	for _, decision := range decisions {
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO policy_decisions (id, candidate_action_id, decision, reason, approval_required, policy_rule_ref, decided_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT(candidate_action_id) DO UPDATE SET
				id = excluded.id,
				decision = excluded.decision,
				reason = excluded.reason,
				approval_required = excluded.approval_required,
				policy_rule_ref = excluded.policy_rule_ref,
				decided_at = excluded.decided_at
		`,
			decision.ID,
			decision.CandidateActionID,
			string(decision.Decision),
			decision.Reason,
			decision.ApprovalRequired,
			decision.PolicyRuleRef,
			decision.DecidedAt.UTC(),
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *PostgresStore) GetPolicyDecision(ctx context.Context, actionID string) (domain.PolicyDecision, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, candidate_action_id, decision, reason, approval_required, policy_rule_ref, decided_at
		FROM policy_decisions
		WHERE candidate_action_id = $1
	`, actionID)

	var decision domain.PolicyDecision
	var decisionType string
	if err := row.Scan(
		&decision.ID,
		&decision.CandidateActionID,
		&decisionType,
		&decision.Reason,
		&decision.ApprovalRequired,
		&decision.PolicyRuleRef,
		&decision.DecidedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.PolicyDecision{}, ErrNotFound
		}
		return domain.PolicyDecision{}, err
	}

	decision.Decision = domain.PolicyDecisionType(decisionType)
	return decision, nil
}

func (s *PostgresStore) ListPolicyDecisions(ctx context.Context, incidentID string) ([]domain.PolicyDecision, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT pd.id, pd.candidate_action_id, pd.decision, pd.reason, pd.approval_required, pd.policy_rule_ref, pd.decided_at
		FROM policy_decisions pd
		INNER JOIN candidate_actions ca ON ca.id = pd.candidate_action_id
		WHERE ca.incident_id = $1
		ORDER BY pd.decided_at ASC
	`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var decisions []domain.PolicyDecision
	for rows.Next() {
		var decision domain.PolicyDecision
		var decisionType string
		if err := rows.Scan(
			&decision.ID,
			&decision.CandidateActionID,
			&decisionType,
			&decision.Reason,
			&decision.ApprovalRequired,
			&decision.PolicyRuleRef,
			&decision.DecidedAt,
		); err != nil {
			return nil, err
		}

		decision.Decision = domain.PolicyDecisionType(decisionType)
		decisions = append(decisions, decision)
	}

	return decisions, rows.Err()
}

func (s *PostgresStore) CreateApprovalRecord(ctx context.Context, record domain.ApprovalRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO approval_records (id, candidate_action_id, approved_by, decision, note, action_digest, policy_version, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`,
		record.ID,
		record.CandidateActionID,
		record.ApprovedBy,
		record.Decision,
		record.Note,
		record.ActionDigest,
		record.PolicyVersion,
		nullableTime(record.ExpiresAt),
		record.CreatedAt.UTC(),
	)
	return err
}

func (s *PostgresStore) ListApprovalRecords(ctx context.Context, incidentID string) ([]domain.ApprovalRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT ar.id, ar.candidate_action_id, ar.approved_by, ar.decision, ar.note, ar.action_digest, ar.policy_version, ar.expires_at, ar.created_at
		FROM approval_records ar
		INNER JOIN candidate_actions ca ON ca.id = ar.candidate_action_id
		WHERE ca.incident_id = $1
		ORDER BY ar.created_at ASC
	`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []domain.ApprovalRecord
	for rows.Next() {
		var record domain.ApprovalRecord
		if err := rows.Scan(
			&record.ID,
			&record.CandidateActionID,
			&record.ApprovedBy,
			&record.Decision,
			&record.Note,
			&record.ActionDigest,
			&record.PolicyVersion,
			&record.ExpiresAt,
			&record.CreatedAt,
		); err != nil {
			return nil, err
		}
		records = append(records, record)
	}

	return records, rows.Err()
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}

func (s *PostgresStore) SaveExecutionRecord(ctx context.Context, record domain.ExecutionRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO execution_records (id, candidate_action_id, idempotency_key, initiated_by, executor_type, status, started_at, finished_at, result_json)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb)
		ON CONFLICT(id) DO UPDATE SET
			idempotency_key = excluded.idempotency_key,
			initiated_by = excluded.initiated_by,
			executor_type = excluded.executor_type,
			status = excluded.status,
			started_at = excluded.started_at,
			finished_at = excluded.finished_at,
			result_json = excluded.result_json
	`,
		record.ID,
		record.CandidateActionID,
		record.IdempotencyKey,
		record.InitiatedBy,
		record.ExecutorType,
		record.Status,
		record.StartedAt.UTC(),
		record.FinishedAt.UTC(),
		record.ResultJSON,
	)
	return err
}

func (s *PostgresStore) ClaimExecution(ctx context.Context, incidentID string, actionID string, record domain.ExecutionRecord) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var actionStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM candidate_actions WHERE id = $1 AND incident_id = $2 FOR UPDATE`, actionID, incidentID).Scan(&actionStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, err
	}
	if actionStatus != string(domain.CandidateActionStatusApproved) && actionStatus != string(domain.CandidateActionStatusAllowed) {
		return false, nil
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO execution_records (id, candidate_action_id, idempotency_key, initiated_by, executor_type, status, started_at, finished_at, result_json)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb)
		ON CONFLICT(candidate_action_id) DO NOTHING
	`, record.ID, record.CandidateActionID, record.IdempotencyKey, record.InitiatedBy, record.ExecutorType, record.Status, record.StartedAt.UTC(), record.FinishedAt.UTC(), record.ResultJSON)
	if err != nil {
		return false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil || inserted == 0 {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE candidate_actions SET status = $1 WHERE id = $2`, string(domain.CandidateActionStatusExecuting), actionID); err != nil {
		return false, err
	}
	incidentResult, err := tx.ExecContext(ctx, `UPDATE incidents SET state = $1, updated_at = $2 WHERE id = $3`, string(domain.IncidentStateExecutingAction), time.Now().UTC(), incidentID)
	if err != nil {
		return false, err
	}
	if rows, rowsErr := incidentResult.RowsAffected(); rowsErr != nil || rows == 0 {
		if rowsErr != nil {
			return false, rowsErr
		}
		return false, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *PostgresStore) ListExecutionRecords(ctx context.Context, incidentID string) ([]domain.ExecutionRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT er.id, er.candidate_action_id, er.idempotency_key, er.initiated_by, er.executor_type, er.status, er.started_at, er.finished_at, er.result_json
		FROM execution_records er
		INNER JOIN candidate_actions ca ON ca.id = er.candidate_action_id
		WHERE ca.incident_id = $1
		ORDER BY er.started_at ASC
	`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []domain.ExecutionRecord
	for rows.Next() {
		var record domain.ExecutionRecord
		if err := rows.Scan(
			&record.ID,
			&record.CandidateActionID,
			&record.IdempotencyKey,
			&record.InitiatedBy,
			&record.ExecutorType,
			&record.Status,
			&record.StartedAt,
			&record.FinishedAt,
			&record.ResultJSON,
		); err != nil {
			return nil, err
		}
		records = append(records, record)
	}

	return records, rows.Err()
}

func (s *PostgresStore) ListExecutionRecordsByAction(ctx context.Context, actionID string) ([]domain.ExecutionRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, candidate_action_id, idempotency_key, initiated_by, executor_type, status, started_at, finished_at, result_json
		FROM execution_records
		WHERE candidate_action_id = $1
		ORDER BY started_at ASC
	`, actionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []domain.ExecutionRecord
	for rows.Next() {
		var record domain.ExecutionRecord
		if err := rows.Scan(
			&record.ID,
			&record.CandidateActionID,
			&record.IdempotencyKey,
			&record.InitiatedBy,
			&record.ExecutorType,
			&record.Status,
			&record.StartedAt,
			&record.FinishedAt,
			&record.ResultJSON,
		); err != nil {
			return nil, err
		}
		records = append(records, record)
	}

	return records, rows.Err()
}

func (s *PostgresStore) SaveVerificationResult(ctx context.Context, result domain.VerificationResult) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO verification_results (id, execution_record_id, status, evidence_json, notes, created_at)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6)
		ON CONFLICT(execution_record_id) DO UPDATE SET
			id = excluded.id,
			status = excluded.status,
			evidence_json = excluded.evidence_json,
			notes = excluded.notes,
			created_at = excluded.created_at
	`,
		result.ID,
		result.ExecutionRecordID,
		result.Status,
		result.EvidenceJSON,
		result.Notes,
		result.CreatedAt.UTC(),
	)
	return err
}

func (s *PostgresStore) GetVerificationResult(ctx context.Context, executionRecordID string) (domain.VerificationResult, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, execution_record_id, status, evidence_json, notes, created_at
		FROM verification_results
		WHERE execution_record_id = $1
	`, executionRecordID)

	var result domain.VerificationResult
	if err := row.Scan(
		&result.ID,
		&result.ExecutionRecordID,
		&result.Status,
		&result.EvidenceJSON,
		&result.Notes,
		&result.CreatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.VerificationResult{}, ErrNotFound
		}
		return domain.VerificationResult{}, err
	}

	return result, nil
}

func (s *PostgresStore) ListVerificationResults(ctx context.Context, incidentID string) ([]domain.VerificationResult, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT vr.id, vr.execution_record_id, vr.status, vr.evidence_json, vr.notes, vr.created_at
		FROM verification_results vr
		INNER JOIN execution_records er ON er.id = vr.execution_record_id
		INNER JOIN candidate_actions ca ON ca.id = er.candidate_action_id
		WHERE ca.incident_id = $1
		ORDER BY vr.created_at ASC
	`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []domain.VerificationResult
	for rows.Next() {
		var result domain.VerificationResult
		if err := rows.Scan(
			&result.ID,
			&result.ExecutionRecordID,
			&result.Status,
			&result.EvidenceJSON,
			&result.Notes,
			&result.CreatedAt,
		); err != nil {
			return nil, err
		}
		results = append(results, result)
	}

	return results, rows.Err()
}

func (s *PostgresStore) ListVerificationResultsByAction(ctx context.Context, actionID string) ([]domain.VerificationResult, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT vr.id, vr.execution_record_id, vr.status, vr.evidence_json, vr.notes, vr.created_at
		FROM verification_results vr
		INNER JOIN execution_records er ON er.id = vr.execution_record_id
		WHERE er.candidate_action_id = $1
		ORDER BY vr.created_at ASC
	`, actionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []domain.VerificationResult
	for rows.Next() {
		var result domain.VerificationResult
		if err := rows.Scan(
			&result.ID,
			&result.ExecutionRecordID,
			&result.Status,
			&result.EvidenceJSON,
			&result.Notes,
			&result.CreatedAt,
		); err != nil {
			return nil, err
		}
		results = append(results, result)
	}

	return results, rows.Err()
}

func (s *PostgresStore) SaveRollbackRecord(ctx context.Context, record domain.RollbackRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO rollback_records (id, candidate_action_id, rollback_action_key, triggered_by, status, started_at, finished_at, result_json, note)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9)
		ON CONFLICT(id) DO UPDATE SET
			rollback_action_key = excluded.rollback_action_key,
			triggered_by = excluded.triggered_by,
			status = excluded.status,
			started_at = excluded.started_at,
			finished_at = excluded.finished_at,
			result_json = excluded.result_json,
			note = excluded.note
	`,
		record.ID,
		record.CandidateActionID,
		record.RollbackActionKey,
		record.TriggeredBy,
		record.Status,
		record.StartedAt.UTC(),
		record.FinishedAt.UTC(),
		record.ResultJSON,
		record.Note,
	)
	return err
}

func (s *PostgresStore) ListRollbackRecords(ctx context.Context, incidentID string) ([]domain.RollbackRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT rr.id, rr.candidate_action_id, rr.rollback_action_key, rr.triggered_by, rr.status, rr.started_at, rr.finished_at, rr.result_json, rr.note
		FROM rollback_records rr
		INNER JOIN candidate_actions ca ON ca.id = rr.candidate_action_id
		WHERE ca.incident_id = $1
		ORDER BY rr.started_at ASC
	`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []domain.RollbackRecord
	for rows.Next() {
		var record domain.RollbackRecord
		if err := rows.Scan(
			&record.ID,
			&record.CandidateActionID,
			&record.RollbackActionKey,
			&record.TriggeredBy,
			&record.Status,
			&record.StartedAt,
			&record.FinishedAt,
			&record.ResultJSON,
			&record.Note,
		); err != nil {
			return nil, err
		}
		records = append(records, record)
	}

	return records, rows.Err()
}

func (s *PostgresStore) ListRollbackRecordsByAction(ctx context.Context, actionID string) ([]domain.RollbackRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, candidate_action_id, rollback_action_key, triggered_by, status, started_at, finished_at, result_json, note
		FROM rollback_records
		WHERE candidate_action_id = $1
		ORDER BY started_at ASC
	`, actionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []domain.RollbackRecord
	for rows.Next() {
		var record domain.RollbackRecord
		if err := rows.Scan(
			&record.ID,
			&record.CandidateActionID,
			&record.RollbackActionKey,
			&record.TriggeredBy,
			&record.Status,
			&record.StartedAt,
			&record.FinishedAt,
			&record.ResultJSON,
			&record.Note,
		); err != nil {
			return nil, err
		}
		records = append(records, record)
	}

	return records, rows.Err()
}
