package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Cyaside/Triovexa/internal/domain"
)

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

	var actionStatus, targetResource string
	if err := tx.QueryRowContext(ctx, `SELECT status, target_resource FROM candidate_actions WHERE id = $1 AND incident_id = $2 FOR UPDATE`, actionID, incidentID).Scan(&actionStatus, &targetResource); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, err
	}
	if actionStatus != string(domain.CandidateActionStatusApproved) && actionStatus != string(domain.CandidateActionStatusAllowed) {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, targetResource); err != nil {
		return false, err
	}
	var targetBusy bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM execution_records er
			JOIN candidate_actions ca ON ca.id = er.candidate_action_id
			WHERE ca.target_resource = $1 AND er.status = 'started'
		)
	`, targetResource).Scan(&targetBusy); err != nil {
		return false, err
	}
	if targetBusy {
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

func (s *PostgresStore) ListExecutionRecordsByStatus(ctx context.Context, status string) ([]domain.ExecutionRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, candidate_action_id, idempotency_key, initiated_by, executor_type, status, started_at, finished_at, result_json
		FROM execution_records WHERE status = $1 ORDER BY started_at ASC
	`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []domain.ExecutionRecord
	for rows.Next() {
		var record domain.ExecutionRecord
		if err := rows.Scan(&record.ID, &record.CandidateActionID, &record.IdempotencyKey, &record.InitiatedBy,
			&record.ExecutorType, &record.Status, &record.StartedAt, &record.FinishedAt, &record.ResultJSON); err != nil {
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
