package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

func (s *PostgresStore) ListRepairCasesForIncident(ctx context.Context, incidentID string) ([]coderepair.Case, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+repairCaseColumns+` FROM repair_cases
		WHERE incident_id=$1 ORDER BY created_at DESC LIMIT 20`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cases []coderepair.Case
	for rows.Next() {
		c, err := scanRepairCase(rows)
		if err != nil {
			return nil, err
		}
		cases = append(cases, c)
	}
	return cases, rows.Err()
}

func (s *PostgresStore) GetLatestRepairAttempt(ctx context.Context, caseID string) (coderepair.Attempt, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM repair_attempts WHERE case_id=$1
		ORDER BY attempt_number DESC LIMIT 1`, caseID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return coderepair.Attempt{}, ErrNotFound
	}
	if err != nil {
		return coderepair.Attempt{}, err
	}
	return s.GetRepairAttempt(ctx, id)
}

func (s *PostgresStore) GetLatestRepairApproval(ctx context.Context, caseID, phase string) (coderepair.Approval, error) {
	var a coderepair.Approval
	err := s.db.QueryRowContext(ctx, `SELECT id,case_id,case_version,phase,actor_id,decision,
		scope_digest,policy_version,expires_at,created_at FROM repair_approvals
		WHERE case_id=$1 AND phase=$2 ORDER BY created_at DESC LIMIT 1`, caseID, phase).
		Scan(&a.ID, &a.CaseID, &a.CaseVersion, &a.Phase, &a.ActorID, &a.Decision,
			&a.ScopeDigest, &a.PolicyVersion, &a.ExpiresAt, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return coderepair.Approval{}, ErrNotFound
	}
	return a, err
}

func (s *PostgresStore) ListRepairDeployments(ctx context.Context, caseID string) ([]RepairDeployment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deploymentColumns+` FROM repair_deployments
		WHERE case_id=$1 ORDER BY observed_at`, caseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var deployments []RepairDeployment
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		deployments = append(deployments, d)
	}
	return deployments, rows.Err()
}
