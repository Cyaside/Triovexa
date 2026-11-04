package repair

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	repairverify "github.com/Cyaside/Triovexa/internal/coderepair/verification"
)

type RepairDeployment = repairverify.Deployment

const deploymentColumns = `id,case_id,environment,revision_sha,deployment_id,observed_at,phase,
	verification_status,baseline_json::text,completed_at,lease_token,lease_until,attempts,telemetry_contract_json::text`

func scanDeployment(row repairScanner) (RepairDeployment, error) {
	var d RepairDeployment
	var baseline, contract sql.NullString
	var completed, leaseUntil sql.NullTime
	err := row.Scan(&d.ID, &d.CaseID, &d.Environment, &d.RevisionSHA,
		&d.DeploymentID, &d.ObservedAt, &d.Phase, &d.VerificationStatus,
		&baseline, &completed, &d.LeaseToken, &leaseUntil, &d.Attempts, &contract)
	if errors.Is(err, sql.ErrNoRows) {
		return RepairDeployment{}, ErrNotFound
	}
	if err != nil {
		return RepairDeployment{}, err
	}
	if baseline.Valid {
		if err := json.Unmarshal([]byte(baseline.String), &d.Baseline); err != nil {
			return RepairDeployment{}, err
		}
	}
	if contract.Valid {
		if err := json.Unmarshal([]byte(contract.String), &d.Contract); err != nil || d.Contract == nil || d.Contract.Validate() != nil {
			return RepairDeployment{}, errors.New("stored telemetry contract is invalid")
		}
	}
	if completed.Valid {
		d.CompletedAt = completed.Time
	}
	if leaseUntil.Valid {
		d.LeaseUntil = leaseUntil.Time
	}
	return d, nil
}

func (s *Store) GetRepairDeployment(ctx context.Context, deploymentID string) (RepairDeployment, error) {
	return scanDeployment(s.db.QueryRowContext(ctx, `SELECT `+deploymentColumns+` FROM repair_deployments WHERE deployment_id=$1`, deploymentID))
}

// StartRepairDeployment records a fresh pre-rollout baseline only for the
// exact revision GitHub reported as merged. A PR alone cannot verify recovery.
func (s *Store) StartRepairDeployment(ctx context.Context, caseID, deploymentID, environment, revision string, baseline repairverify.Sample, now time.Time) (bool, error) {
	if caseID == "" || deploymentID == "" || environment == "" || !coderepair.ValidGitRevision(revision) ||
		now.IsZero() || !baseline.Valid(now) || baseline.DeployedRevision == revision {
		return false, errors.New("deployment requires a fresh pre-rollout baseline and new revision")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	c, err := scanRepairCase(tx.QueryRowContext(ctx, `SELECT `+repairCaseColumns+` FROM repair_cases WHERE id=$1 FOR UPDATE`, caseID))
	if err != nil {
		return false, err
	}
	if c.State != coderepair.StateMerged {
		return false, nil
	}
	p, err := scanPublication(tx.QueryRowContext(ctx, `SELECT `+publicationColumns+` FROM repair_publications WHERE case_id=$1`, caseID))
	if err != nil {
		return false, err
	}
	if p.State != coderepair.PublicationMerged || p.MergeSHA != revision {
		return false, errors.New("deployment revision does not match merged PR")
	}
	contract, err := deploymentContractTx(ctx, tx, c, environment)
	if err != nil {
		return false, err
	}
	if !baseline.Matches(contract) {
		return false, errors.New("deployment environment or binding changed")
	}
	var contractJSON any
	if contract != nil {
		data, err := json.Marshal(contract)
		if err != nil {
			return false, err
		}
		contractJSON = string(data)
	}
	content, err := json.Marshal(baseline)
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO repair_deployments
		(id,case_id,environment,revision_sha,deployment_id,observed_at,verification_status,created_at,phase,baseline_json,telemetry_contract_json)
		VALUES ($1,$2,$3,$4,$5,$6,'pending',$6,'started',$7::jsonb,$8::jsonb)`,
		uuid.NewString(), caseID, environment, revision, deploymentID, now.UTC(), string(content), contractJSON); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE repair_cases SET state='awaiting_deployment',version=version+1,updated_at=$1 WHERE id=$2`, now.UTC(), caseID); err != nil {
		return false, err
	}
	if err := insertRepairEventTx(ctx, tx, repairEventActor(caseID, "deployment-pipeline", "deployment_started", deploymentID, now)); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *Store) CompleteRepairDeployment(ctx context.Context, caseID, deploymentID, environment, revision string, now time.Time) (bool, error) {
	if caseID == "" || deploymentID == "" || environment == "" || !coderepair.ValidGitRevision(revision) || now.IsZero() {
		return false, errors.New("invalid deployment completion")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	c, err := scanRepairCase(tx.QueryRowContext(ctx, `SELECT `+repairCaseColumns+` FROM repair_cases WHERE id=$1 FOR UPDATE`, caseID))
	if err != nil {
		return false, err
	}
	if c.State != coderepair.StateAwaitingDeployment {
		return false, nil
	}
	d, err := scanDeployment(tx.QueryRowContext(ctx, `SELECT `+deploymentColumns+` FROM repair_deployments WHERE deployment_id=$1 FOR UPDATE`, deploymentID))
	if err != nil {
		return false, err
	}
	if d.CaseID != caseID || d.Environment != environment || d.RevisionSHA != revision || d.Phase != "started" || now.Before(d.ObservedAt) {
		return false, errors.New("deployment completion does not match its start event")
	}
	contract, err := deploymentContractTx(ctx, tx, c, environment)
	if err != nil || contract.Digest() != d.Contract.Digest() {
		return false, errors.New("deployment telemetry profile changed")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE repair_deployments SET phase='completed',completed_at=$1 WHERE deployment_id=$2`, now.UTC(), deploymentID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE repair_cases SET state='verifying',version=version+1,updated_at=$1 WHERE id=$2`, now.UTC(), caseID); err != nil {
		return false, err
	}
	if err := insertRepairEventTx(ctx, tx, repairEventActor(caseID, "deployment-pipeline", "deployment_completed", deploymentID, now)); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *Store) ClaimRepairVerification(ctx context.Context, now time.Time, lease time.Duration) (RepairDeployment, error) {
	if now.IsZero() || lease <= 0 {
		return RepairDeployment{}, errors.New("invalid verification lease")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RepairDeployment{}, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT d.deployment_id FROM repair_deployments d JOIN repair_cases c ON c.id=d.case_id
		WHERE c.state='verifying' AND d.phase='completed' AND d.verification_status='pending'
		AND (d.lease_until IS NULL OR d.lease_until<=$1)
		ORDER BY d.completed_at FOR UPDATE OF d SKIP LOCKED LIMIT 1`, now.UTC()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return RepairDeployment{}, coderepair.ErrNoJobAvailable
	}
	if err != nil {
		return RepairDeployment{}, err
	}
	token := uuid.NewString()
	d, err := scanDeployment(tx.QueryRowContext(ctx, `UPDATE repair_deployments SET lease_token=$1,
		lease_until=$2,attempts=attempts+1 WHERE deployment_id=$3 RETURNING `+deploymentColumns,
		token, now.Add(lease).UTC(), id))
	if err != nil {
		return RepairDeployment{}, err
	}
	return d, tx.Commit()
}

func (s *Store) RecordRepairVerificationSample(ctx context.Context, d RepairDeployment, sample repairverify.Sample, passed bool, now time.Time) error {
	if d.DeploymentID == "" || d.LeaseToken == "" || now.IsZero() {
		return errors.New("invalid repair verification identity")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := scanDeployment(tx.QueryRowContext(ctx, `SELECT `+deploymentColumns+` FROM repair_deployments WHERE deployment_id=$1 FOR UPDATE`, d.DeploymentID))
	if err != nil {
		return err
	}
	if current.LeaseToken != d.LeaseToken || !current.LeaseUntil.After(now) || current.VerificationStatus != "pending" {
		return ErrRepairConflict
	}
	c, err := scanRepairCase(tx.QueryRowContext(ctx, `SELECT `+repairCaseColumns+` FROM repair_cases WHERE id=$1`, current.CaseID))
	if err != nil {
		return err
	}
	if c.State != coderepair.StateVerifying {
		return ErrRepairConflict
	}
	contract, err := deploymentContractTx(ctx, tx, c, current.Environment)
	if err != nil || contract.Digest() != current.Contract.Digest() || !sample.Valid(now) || !sample.Matches(current.Contract) ||
		!sample.Timestamp.After(current.CompletedAt) || passed != repairverify.CheckFor(current.Contract, current.Baseline, sample, current.RevisionSHA) {
		return errors.New("invalid repair verification sample or changed binding")
	}
	content, err := json.Marshal(sample)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO repair_verification_samples (deployment_id,observation_number,captured_at,sample_json,passed)
 VALUES ($1,COALESCE((SELECT max(observation_number)+1 FROM repair_verification_samples WHERE deployment_id=$1),1),$2,$3::jsonb,$4)`, current.DeploymentID, now.UTC(), string(content), passed)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FinishRepairVerification(ctx context.Context, d RepairDeployment, recovered bool, reason string, now time.Time) (bool, error) {
	if d.DeploymentID == "" || d.LeaseToken == "" || now.IsZero() {
		return false, errors.New("invalid verification result")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	current, err := scanDeployment(tx.QueryRowContext(ctx, `SELECT `+deploymentColumns+` FROM repair_deployments WHERE deployment_id=$1 FOR UPDATE`, d.DeploymentID))
	if err != nil {
		return false, err
	}
	if current.VerificationStatus != "pending" || current.LeaseToken != d.LeaseToken || !current.LeaseUntil.After(now) {
		return false, nil
	}
	c, err := scanRepairCase(tx.QueryRowContext(ctx, `SELECT `+repairCaseColumns+` FROM repair_cases WHERE id=$1 FOR UPDATE`, current.CaseID))
	if err != nil {
		return false, err
	}
	if c.State != coderepair.StateVerifying {
		return false, nil
	}
	contract, err := deploymentContractTx(ctx, tx, c, current.Environment)
	if err != nil || contract.Digest() != current.Contract.Digest() {
		if recovered {
			return false, errors.New("verification telemetry profile changed")
		}
		reason = "telemetry binding disabled or changed; recovery is unproven"
	}
	if recovered {
		rows, err := tx.QueryContext(ctx, `SELECT sample_json::text,captured_at,passed FROM repair_verification_samples
			WHERE deployment_id=$1 ORDER BY observation_number DESC LIMIT 3`, d.DeploymentID)
		if err != nil {
			return false, err
		}
		var samples []repairverify.Sample
		var times []time.Time
		for rows.Next() {
			var raw string
			var at time.Time
			var passed bool
			if err := rows.Scan(&raw, &at, &passed); err != nil {
				rows.Close()
				return false, err
			}
			var sample repairverify.Sample
			if err := json.Unmarshal([]byte(raw), &sample); err != nil {
				rows.Close()
				return false, err
			}
			if !passed || !sample.Valid(at) || !sample.Timestamp.After(current.CompletedAt) ||
				(!sample.Matches(current.Contract) || !repairverify.CheckFor(current.Contract, current.Baseline, sample, current.RevisionSHA)) {
				rows.Close()
				return false, errors.New("recovery samples do not match deployed revision")
			}
			samples = append(samples, sample)
			times = append(times, at)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return false, err
		}
		rows.Close()
		if len(samples) != 3 || times[0].Sub(times[1]) < 10*time.Second || times[1].Sub(times[2]) < 10*time.Second ||
			samples[0].Timestamp.Sub(samples[1].Timestamp) < 10*time.Second ||
			samples[1].Timestamp.Sub(samples[2].Timestamp) < 10*time.Second {
			return false, errors.New("recovery requires three spaced healthy observations")
		}
	}
	status, next := "inconclusive", coderepair.StateInconclusive
	if recovered {
		status, next = "recovered", coderepair.StateRecovered
	}
	result, _ := json.Marshal(map[string]any{"status": status, "reason": redactRepairError(reason), "finished_at": now.UTC()})
	if _, err := tx.ExecContext(ctx, `UPDATE repair_deployments SET verification_status=$1,result_json=$2::jsonb,
		lease_token='',lease_until=NULL WHERE deployment_id=$3`, status, string(result), d.DeploymentID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE repair_cases SET state=$1,version=version+1,updated_at=$2 WHERE id=$3`, next, now.UTC(), current.CaseID); err != nil {
		return false, err
	}
	if err := insertRepairEventTx(ctx, tx, repairEventActor(current.CaseID, "repair-verifier", "deployment_verification", status+": "+reason, now)); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
