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

func (s *PostgresStore) DecideApproval(ctx context.Context, incidentID string, record domain.ApprovalRecord, expectedAction, nextAction domain.CandidateActionStatus, expectedIncident, nextIncident domain.IncidentState) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var actionStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM candidate_actions WHERE id = $1 AND incident_id = $2 FOR UPDATE`, record.CandidateActionID, incidentID).Scan(&actionStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, err
	}
	if actionStatus != string(expectedAction) {
		return false, nil
	}
	if expectedIncident != "" {
		var incidentState string
		if err := tx.QueryRowContext(ctx, `SELECT state FROM incidents WHERE id = $1 FOR UPDATE`, incidentID).Scan(&incidentState); err != nil {
			return false, err
		}
		if incidentState != string(expectedIncident) {
			return false, nil
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO approval_records (id, candidate_action_id, approved_by, decision, note, action_digest, policy_version, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, record.ID, record.CandidateActionID, record.ApprovedBy, record.Decision, record.Note, record.ActionDigest,
		record.PolicyVersion, nullableTime(record.ExpiresAt), record.CreatedAt.UTC()); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE candidate_actions SET status = $1 WHERE id = $2`, string(nextAction), record.CandidateActionID); err != nil {
		return false, err
	}
	if nextIncident != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE incidents SET state = $1, updated_at = $2 WHERE id = $3`, string(nextIncident), time.Now().UTC(), incidentID); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *PostgresStore) ListApprovalRecords(ctx context.Context, incidentID string) ([]domain.ApprovalRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT ar.id, ar.candidate_action_id, ar.approved_by, ar.decision, ar.note, ar.action_digest, ar.policy_version, COALESCE(ar.expires_at, ar.created_at), ar.created_at
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
