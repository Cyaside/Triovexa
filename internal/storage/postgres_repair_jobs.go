package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

const repairJobColumns = `id,case_id,attempt_id,type,dedup_key,payload_json::text,status,
	attempts,max_attempts,available_at,lease_owner,lease_token,lease_until,last_error,created_at,updated_at`

const repairJobReturning = `j.id,j.case_id,j.attempt_id,j.type,j.dedup_key,j.payload_json::text,j.status,
	j.attempts,j.max_attempts,j.available_at,j.lease_owner,j.lease_token,j.lease_until,j.last_error,j.created_at,j.updated_at`

func scanRepairJob(row repairScanner) (coderepair.Job, error) {
	var job coderepair.Job
	var leaseUntil sql.NullTime
	err := row.Scan(&job.ID, &job.CaseID, &job.AttemptID, &job.Type, &job.DedupKey,
		&job.PayloadJSON, &job.Status, &job.Attempts, &job.MaxAttempts, &job.AvailableAt,
		&job.LeaseOwner, &job.LeaseToken, &leaseUntil, &job.LastError, &job.CreatedAt, &job.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return coderepair.Job{}, coderepair.ErrNoJobAvailable
	}
	if leaseUntil.Valid {
		job.LeaseUntil = leaseUntil.Time
	}
	return job, err
}

func (s *PostgresStore) GetRepairJob(ctx context.Context, id string) (coderepair.Job, error) {
	job, err := scanRepairJob(s.db.QueryRowContext(ctx, `SELECT `+repairJobColumns+` FROM repair_jobs WHERE id=$1`, id))
	if errors.Is(err, coderepair.ErrNoJobAvailable) {
		return coderepair.Job{}, ErrNotFound
	}
	return job, err
}

// ClaimRepairJob fences stale workers with a fresh lease token on every claim.
// A lease can be reclaimed after expiry, but not when the attempt budget is
// exhausted. RecoverRepairJobs records that terminal state at startup.
func (s *PostgresStore) ClaimRepairJob(ctx context.Context, workerID string, now time.Time, lease time.Duration) (coderepair.Job, error) {
	if workerID == "" || lease <= 0 || now.IsZero() {
		return coderepair.Job{}, errors.New("invalid repair job lease")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return coderepair.Job{}, err
	}
	defer tx.Rollback()
	var jobID string
	err = tx.QueryRowContext(ctx, `SELECT j.id FROM repair_jobs j JOIN repair_cases c ON c.id=j.case_id
		WHERE c.state='investigating' AND j.attempts < j.max_attempts AND j.available_at <= $1
		  AND (j.status='queued' OR (j.status='running' AND j.lease_until <= $1))
		ORDER BY j.available_at,j.created_at FOR UPDATE OF j,c SKIP LOCKED LIMIT 1`, now.UTC()).Scan(&jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return coderepair.Job{}, coderepair.ErrNoJobAvailable
	}
	if err != nil {
		return coderepair.Job{}, err
	}
	leaseToken := uuid.NewString()
	job, err := scanRepairJob(tx.QueryRowContext(ctx, `UPDATE repair_jobs j SET status='running',
		attempts=j.attempts+1,lease_owner=$1,lease_token=$2,lease_until=$3,updated_at=$4
		WHERE j.id=$5 RETURNING `+repairJobReturning,
		workerID, leaseToken, now.Add(lease).UTC(), now.UTC(), jobID))
	if err != nil {
		return coderepair.Job{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE repair_attempts SET status='running',started_at=COALESCE(started_at,$1)
		WHERE id=$2 AND case_id=$3 AND status IN ('queued','running')`, now.UTC(), job.AttemptID, job.CaseID)
	if err != nil {
		return coderepair.Job{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return coderepair.Job{}, err
	}
	if changed != 1 {
		return coderepair.Job{}, errors.New("repair job attempt is no longer claimable")
	}
	if err := tx.Commit(); err != nil {
		return coderepair.Job{}, err
	}
	return job, nil
}

func (s *PostgresStore) RenewRepairJobLease(ctx context.Context, jobID, leaseToken string, now time.Time, lease time.Duration) (bool, error) {
	if jobID == "" || leaseToken == "" || now.IsZero() || lease <= 0 {
		return false, errors.New("invalid repair job lease renewal")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE repair_jobs SET lease_until=GREATEST(lease_until,$1), updated_at=$2
		WHERE id=$3 AND status='running' AND lease_token=$4 AND lease_until > $2`,
		now.Add(lease).UTC(), now.UTC(), jobID, leaseToken)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func (s *PostgresStore) CompleteRepairJob(ctx context.Context, jobID, leaseToken string, now time.Time) (bool, error) {
	if jobID == "" || leaseToken == "" || now.IsZero() {
		return false, errors.New("invalid repair job completion")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	job, err := scanRepairJob(tx.QueryRowContext(ctx, `SELECT `+repairJobColumns+` FROM repair_jobs WHERE id=$1 FOR UPDATE`, jobID))
	if err != nil {
		return false, err
	}
	if job.Status != coderepair.JobRunning || job.LeaseToken != leaseToken || !job.LeaseUntil.After(now) {
		return false, nil
	}
	var state coderepair.State
	if err := tx.QueryRowContext(ctx, `SELECT state FROM repair_cases WHERE id=$1`, job.CaseID).Scan(&state); err != nil {
		return false, err
	}
	if !repairCaseHasCompletedInvestigation(state) {
		return false, errors.New("repair handler returned without a durable investigation outcome")
	}
	attemptStatus := "succeeded"
	if state == coderepair.StateBlocked || state == coderepair.StateFailed {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM repair_artifacts
			WHERE attempt_id=$1 AND kind='investigation_report')`, job.AttemptID).Scan(&exists); err != nil {
			return false, err
		}
		if !exists {
			return false, errors.New("repair failure has no durable investigation report")
		}
		attemptStatus = string(state)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE repair_jobs SET status='succeeded',lease_owner='',lease_token='',
		lease_until=NULL,updated_at=$1 WHERE id=$2`, now.UTC(), jobID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE repair_attempts SET status=$1,finished_at=$2
		WHERE id=$3 AND status IN ('queued','running')`, attemptStatus, now.UTC(), job.AttemptID); err != nil {
		return false, err
	}
	if err := insertRepairEventTx(ctx, tx, repairEvent(job.CaseID, "investigation_job_completed", "patch outcome persisted", now)); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// FailRepairJob requeues a transient failure or records a terminal failure in
// the same transaction as the case/attempt state and audit event.
func (s *PostgresStore) FailRepairJob(ctx context.Context, jobID, leaseToken, message string, now, retryAt time.Time, terminal bool) (bool, error) {
	if jobID == "" || leaseToken == "" || now.IsZero() || (!terminal && retryAt.Before(now)) {
		return false, errors.New("invalid repair job failure")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	job, err := scanRepairJob(tx.QueryRowContext(ctx, `SELECT `+repairJobColumns+` FROM repair_jobs WHERE id=$1 FOR UPDATE`, jobID))
	if err != nil {
		return false, err
	}
	if job.Status != coderepair.JobRunning || job.LeaseToken != leaseToken || !job.LeaseUntil.After(now) {
		return false, nil
	}
	status := coderepair.JobQueued
	if terminal || job.Attempts >= job.MaxAttempts {
		status = coderepair.JobDeadLetter
	}
	_, err = tx.ExecContext(ctx, `UPDATE repair_jobs SET status=$1, available_at=$2, lease_owner='',
		lease_token='',lease_until=NULL,last_error=$3,updated_at=$4 WHERE id=$5`, status,
		retryAt.UTC(), redactRepairError(message), now.UTC(), jobID)
	if err != nil {
		return false, err
	}
	if status == coderepair.JobDeadLetter {
		if err := blockRepairAttemptTx(ctx, tx, job.CaseID, job.AttemptID, message, now); err != nil {
			return false, err
		}
	} else {
		result, err := tx.ExecContext(ctx, `UPDATE repair_attempts SET status='queued'
			WHERE id=$1 AND case_id=$2 AND status='running'`, job.AttemptID, job.CaseID)
		if err != nil {
			return false, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return false, err
		}
		if changed != 1 {
			return false, errors.New("repair attempt could not be requeued")
		}
	}
	return true, tx.Commit()
}

func blockRepairAttemptTx(ctx context.Context, tx *sql.Tx, caseID, attemptID, reason string, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE repair_attempts SET status='blocked',error_code='ATTEMPTS_EXHAUSTED',
		error_message=$1,finished_at=$2 WHERE id=$3 AND status IN ('queued','running')`,
		redactRepairError(reason), now.UTC(), attemptID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE repair_cases SET state='blocked',version=version+1,updated_at=$1
		WHERE id=$2 AND state='investigating'`, now.UTC(), caseID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 1 {
		return insertRepairEventTx(ctx, tx, repairEvent(caseID, "investigation_blocked", reason, now))
	}
	return nil
}

// RecoverRepairJobs is safe to call at startup. It reconciles a handler that
// persisted a case outcome but crashed before completing its job, and
// dead-letters expired exhausted leases. Other expired jobs remain claimable.
// It never steals an unexpired lease from another process.
func (s *PostgresStore) RecoverRepairJobs(ctx context.Context, now time.Time) (int, error) {
	if now.IsZero() {
		return 0, errors.New("repair recovery requires a time")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT j.id,j.case_id,j.attempt_id,c.state
		FROM repair_jobs j JOIN repair_cases c ON c.id=j.case_id
		WHERE (j.status='running' AND j.lease_until <= $1 AND (j.attempts >= j.max_attempts OR c.state <> 'investigating'))
		   OR (j.status='queued' AND c.state <> 'investigating')
		FOR UPDATE OF j SKIP LOCKED`, now.UTC())
	if err != nil {
		return 0, err
	}
	type recoveryCandidate struct {
		id, caseID, attemptID string
		caseState             coderepair.State
	}
	var jobs []recoveryCandidate
	for rows.Next() {
		var job recoveryCandidate
		if err := rows.Scan(&job.id, &job.caseID, &job.attemptID, &job.caseState); err != nil {
			rows.Close()
			return 0, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	for _, job := range jobs {
		if job.caseState != coderepair.StateInvestigating {
			status := coderepair.JobDeadLetter
			attemptStatus := "blocked"
			if repairCaseHasCompletedInvestigation(job.caseState) {
				status = coderepair.JobSucceeded
				attemptStatus = "succeeded"
				if job.caseState == coderepair.StateBlocked || job.caseState == coderepair.StateFailed {
					var exists bool
					if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM repair_artifacts
						WHERE attempt_id=$1 AND kind='investigation_report')`, job.attemptID).Scan(&exists); err != nil {
						return 0, err
					}
					if exists {
						attemptStatus = string(job.caseState)
					} else {
						status = coderepair.JobDeadLetter
						attemptStatus = "blocked"
					}
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE repair_jobs SET status=$1,lease_owner='',lease_token='',
				lease_until=NULL,updated_at=$2 WHERE id=$3`, status, now.UTC(), job.id); err != nil {
				return 0, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE repair_attempts SET status=$1,finished_at=$2
				WHERE id=$3 AND status IN ('queued','running')`, attemptStatus, now.UTC(), job.attemptID); err != nil {
				return 0, err
			}
			if err := insertRepairEventTx(ctx, tx, repairEvent(job.caseID, "repair_job_reconciled",
				"case already moved to "+string(job.caseState), now)); err != nil {
				return 0, err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE repair_jobs SET status='dead_letter',lease_owner='',lease_token='',
			lease_until=NULL,last_error=$1,updated_at=$2 WHERE id=$3`,
			"lease expired after final attempt", now.UTC(), job.id); err != nil {
			return 0, fmt.Errorf("dead-letter repair job %s: %w", job.id, err)
		}
		if err := blockRepairAttemptTx(ctx, tx, job.caseID, job.attemptID,
			"lease expired after final attempt", now); err != nil {
			return 0, err
		}
	}
	return len(jobs), tx.Commit()
}

func repairCaseHasCompletedInvestigation(state coderepair.State) bool {
	switch state {
	case coderepair.StatePatchReady, coderepair.StateBlocked, coderepair.StateFailed,
		coderepair.StateAwaitingPublishApproval,
		coderepair.StatePublishing, coderepair.StatePROpen, coderepair.StateMerged,
		coderepair.StateAwaitingDeployment, coderepair.StateVerifying,
		coderepair.StateRecovered, coderepair.StateInconclusive:
		return true
	default:
		return false
	}
}
