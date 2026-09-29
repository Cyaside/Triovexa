package storage

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/security"
)

var ErrRepairConflict = errors.New("storage: repair case changed")

var (
	_ coderepair.CaseStore     = (*PostgresStore)(nil)
	_ coderepair.ArtifactStore = (*PostgresStore)(nil)
	_ coderepair.JobStore      = (*PostgresStore)(nil)
)

type repairScanner interface{ Scan(...any) error }

func (s *PostgresStore) CreateRepositoryBinding(ctx context.Context, binding coderepair.RepositoryBinding) error {
	if err := binding.Validate(); err != nil {
		return err
	}
	paths, err := json.Marshal(binding.AllowedPaths)
	if err != nil {
		return err
	}
	recipes, err := json.Marshal(binding.TestRecipes)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO repository_bindings
		(id, service_name, environment, repository_url, base_ref, allowed_paths_json, test_recipes_json, policy_version, enabled, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		binding.ID, binding.ServiceName, binding.Environment, binding.RepositoryURL, binding.BaseRef,
		string(paths), string(recipes), binding.PolicyVersion, binding.Enabled, binding.CreatedAt.UTC(), binding.UpdatedAt.UTC())
	return err
}

func scanRepositoryBinding(row repairScanner) (coderepair.RepositoryBinding, error) {
	var binding coderepair.RepositoryBinding
	var paths, recipes string
	err := row.Scan(&binding.ID, &binding.ServiceName, &binding.Environment, &binding.RepositoryURL,
		&binding.BaseRef, &paths, &recipes, &binding.PolicyVersion, &binding.Enabled, &binding.CreatedAt, &binding.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return coderepair.RepositoryBinding{}, ErrNotFound
	}
	if err != nil {
		return coderepair.RepositoryBinding{}, err
	}
	if err := json.Unmarshal([]byte(paths), &binding.AllowedPaths); err != nil {
		return coderepair.RepositoryBinding{}, err
	}
	if err := json.Unmarshal([]byte(recipes), &binding.TestRecipes); err != nil {
		return coderepair.RepositoryBinding{}, err
	}
	return binding, nil
}

const repairBindingColumns = `id, service_name, environment, repository_url, base_ref,
	allowed_paths_json::text, test_recipes_json::text, policy_version, enabled, created_at, updated_at`

func (s *PostgresStore) GetActiveRepositoryBinding(ctx context.Context, service, environment string) (coderepair.RepositoryBinding, error) {
	return scanRepositoryBinding(s.db.QueryRowContext(ctx, `SELECT `+repairBindingColumns+`
		FROM repository_bindings WHERE service_name=$1 AND environment=$2 AND enabled`, service, environment))
}

func (s *PostgresStore) GetRepositoryBinding(ctx context.Context, id string) (coderepair.RepositoryBinding, error) {
	return scanRepositoryBinding(s.db.QueryRowContext(ctx, `SELECT `+repairBindingColumns+`
		FROM repository_bindings WHERE id=$1`, id))
}

func getRepairBindingTx(ctx context.Context, tx *sql.Tx, id string) (coderepair.RepositoryBinding, error) {
	return scanRepositoryBinding(tx.QueryRowContext(ctx, `SELECT `+repairBindingColumns+`
		FROM repository_bindings WHERE id=$1`, id))
}

func (s *PostgresStore) CreateRepairCase(ctx context.Context, repairCase coderepair.Case, event coderepair.Event) error {
	if repairCase.State != coderepair.StateProposed {
		return errors.New("direct case creation requires proposed state")
	}
	return s.createRepairCase(ctx, repairCase, event, nil)
}

// CreateRepairProposal commits the approval-ready case, audit and sanitized
// evidence snapshot together. A case cannot be approved without its snapshot.
func (s *PostgresStore) CreateRepairProposal(ctx context.Context, repairCase coderepair.Case, event coderepair.Event, snapshot coderepair.EvidenceSnapshot) error {
	if repairCase.State != coderepair.StateAwaitingInvestigationApproval || !snapshot.VerifyDigest() ||
		snapshot.IncidentID != repairCase.IncidentID || snapshot.DeployedRevision != repairCase.DeployedSHA ||
		snapshot.CapturedAt.IsZero() || time.Since(snapshot.CapturedAt) > time.Minute ||
		snapshot.CapturedAt.After(time.Now().Add(5*time.Second)) {
		return errors.New("repair proposal requires fresh matching evidence snapshot")
	}
	return s.createRepairCase(ctx, repairCase, event, &snapshot)
}

func (s *PostgresStore) createRepairCase(ctx context.Context, repairCase coderepair.Case, event coderepair.Event, snapshot *coderepair.EvidenceSnapshot) error {
	if repairCase.ID == "" || repairCase.IncidentID == "" || repairCase.BindingID == "" || repairCase.CreatedBy == "" ||
		(repairCase.State != coderepair.StateProposed && repairCase.State != coderepair.StateAwaitingInvestigationApproval) ||
		repairCase.Version != 1 || repairCase.CreatedAt.IsZero() || repairCase.UpdatedAt.IsZero() {
		return errors.New("invalid initial repair case")
	}
	if err := validateRepairEvent(event, repairCase.ID); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	binding, err := getRepairBindingTx(ctx, tx, repairCase.BindingID)
	if err != nil {
		return err
	}
	if !binding.Enabled {
		return errors.New("repository binding is disabled")
	}
	var service, environment, incidentState string
	if err := tx.QueryRowContext(ctx, `SELECT service_name, environment, state FROM incidents WHERE id=$1`, repairCase.IncidentID).
		Scan(&service, &environment, &incidentState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if service != binding.ServiceName || environment != binding.Environment {
		return errors.New("incident target does not match repository binding")
	}
	if snapshot != nil && (snapshot.ServiceName != service || snapshot.Environment != environment) {
		return errors.New("repair evidence target does not match incident")
	}
	if incidentState != "escalated" && incidentState != "failed_remediation" {
		return errors.New("incident is not eligible for code repair")
	}
	if snapshot != nil {
		incident := domain.Incident{ID: repairCase.IncidentID, ServiceName: service,
			Environment: environment, State: domain.IncidentState(incidentState)}
		if err := coderepair.CheckInvestigationEvidence(incident, binding, *snapshot, time.Now().UTC()); err != nil {
			return err
		}
	}
	if !coderepair.ValidGitRevision(repairCase.BaseSHA) || !coderepair.ValidGitRevision(repairCase.DeployedSHA) {
		return errors.New("repair case requires complete Git revision IDs")
	}
	deployed, err := trustedDeployedRevisionTx(ctx, tx, repairCase.IncidentID, service, time.Now().UTC())
	if err != nil || deployed != repairCase.DeployedSHA {
		return errors.New("repair case deployed revision does not match fresh workload evidence")
	}
	if snapshot != nil {
		freshLog, err := hasFreshRepairLogTx(ctx, tx, repairCase.IncidentID, time.Now().UTC())
		if err != nil {
			return err
		}
		if !freshLog {
			return errors.New("repair proposal requires fresh stored log evidence")
		}
	}
	digest, err := coderepair.ScopeDigest(repairCase, binding)
	if err != nil || digest != repairCase.ScopeDigest {
		return errors.New("repair case scope does not match active binding")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO repair_cases
		(id,incident_id,binding_id,base_sha,deployed_sha,scope_digest,policy_version,state,version,created_by,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, repairCase.ID, repairCase.IncidentID,
		repairCase.BindingID, repairCase.BaseSHA, repairCase.DeployedSHA, repairCase.ScopeDigest,
		repairCase.PolicyVersion, repairCase.State, repairCase.Version, repairCase.CreatedBy,
		repairCase.CreatedAt.UTC(), repairCase.UpdatedAt.UTC()); err != nil {
		return err
	}
	if err := insertRepairEventTx(ctx, tx, event); err != nil {
		return err
	}
	if snapshot != nil {
		content, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO repair_evidence
			(id,case_id,source,observed_at,complete,content_sha256,artifact_ref,created_at,snapshot_json)
			VALUES ($1,$2,'incident_snapshot',$3,true,$4,'database',$5,$6::jsonb)`,
			uuid.NewString(), repairCase.ID, snapshot.CapturedAt.UTC(), snapshot.SHA256,
			repairCase.CreatedAt.UTC(), string(content)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *PostgresStore) GetRepairEvidenceSnapshot(ctx context.Context, caseID string) (coderepair.EvidenceSnapshot, error) {
	var content, digest, incidentID string
	err := s.db.QueryRowContext(ctx, `SELECT e.snapshot_json::text,e.content_sha256,c.incident_id
		FROM repair_evidence e JOIN repair_cases c ON c.id=e.case_id
		WHERE e.case_id=$1 AND e.source='incident_snapshot'`, caseID).Scan(&content, &digest, &incidentID)
	if errors.Is(err, sql.ErrNoRows) {
		return coderepair.EvidenceSnapshot{}, ErrNotFound
	}
	if err != nil {
		return coderepair.EvidenceSnapshot{}, err
	}
	var snapshot coderepair.EvidenceSnapshot
	if err := json.Unmarshal([]byte(content), &snapshot); err != nil || !snapshot.VerifyDigest() ||
		snapshot.SHA256 != digest || snapshot.IncidentID != incidentID {
		return coderepair.EvidenceSnapshot{}, errors.New("repair evidence snapshot is corrupt")
	}
	return snapshot, nil
}

func trustedDeployedRevisionTx(ctx context.Context, tx *sql.Tx, incidentID, service string, now time.Time) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,incident_id,type,source,snippet,timestamp,metadata_json::text
		FROM evidence_items WHERE incident_id=$1 AND source='workload-control' AND type='metric'
		AND timestamp >= $2 ORDER BY timestamp DESC LIMIT 20`, incidentID, now.Add(-time.Minute))
	if err != nil {
		return "", err
	}
	var items []domain.EvidenceItem
	for rows.Next() {
		var item domain.EvidenceItem
		if err := rows.Scan(&item.ID, &item.IncidentID, &item.Type, &item.Source, &item.Snippet, &item.Timestamp, &item.MetadataJSON); err != nil {
			rows.Close()
			return "", err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", err
	}
	rows.Close()
	return coderepair.TrustedDeployedRevision(domain.Incident{ID: incidentID, ServiceName: service}, items, now)
}

func hasFreshRepairLogTx(ctx context.Context, tx *sql.Tx, incidentID string, now time.Time) (bool, error) {
	var fresh bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM evidence_items
		WHERE incident_id=$1 AND type='log' AND snippet<>'' AND timestamp >= $2 AND timestamp <= $3)`,
		incidentID, now.Add(-time.Minute), now.Add(5*time.Second)).Scan(&fresh)
	return fresh, err
}

const repairCaseColumns = `id, incident_id, binding_id, base_sha, deployed_sha, scope_digest,
	policy_version, state, version, created_by, created_at, updated_at`

func scanRepairCase(row repairScanner) (coderepair.Case, error) {
	var c coderepair.Case
	err := row.Scan(&c.ID, &c.IncidentID, &c.BindingID, &c.BaseSHA, &c.DeployedSHA,
		&c.ScopeDigest, &c.PolicyVersion, &c.State, &c.Version, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return coderepair.Case{}, ErrNotFound
	}
	return c, err
}

func (s *PostgresStore) GetRepairCase(ctx context.Context, id string) (coderepair.Case, error) {
	return scanRepairCase(s.db.QueryRowContext(ctx, `SELECT `+repairCaseColumns+` FROM repair_cases WHERE id=$1`, id))
}

func (s *PostgresStore) GetRepairAttempt(ctx context.Context, id string) (coderepair.Attempt, error) {
	var attempt coderepair.Attempt
	var started, finished sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT id,case_id,attempt_number,status,provider,model,prompt_version,
		error_code,error_message,started_at,finished_at,created_at FROM repair_attempts WHERE id=$1`, id).
		Scan(&attempt.ID, &attempt.CaseID, &attempt.Number, &attempt.Status, &attempt.Provider, &attempt.Model,
			&attempt.PromptVersion, &attempt.ErrorCode, &attempt.ErrorMessage, &started, &finished, &attempt.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return coderepair.Attempt{}, ErrNotFound
	}
	if started.Valid {
		attempt.StartedAt = started.Time
	}
	if finished.Valid {
		attempt.FinishedAt = finished.Time
	}
	return attempt, err
}

func (s *PostgresStore) TransitionRepairCase(ctx context.Context, id string, expected coderepair.State, version int64, next coderepair.State, event coderepair.Event) (bool, error) {
	if !coderepair.CanTransition(expected, next) {
		return false, fmt.Errorf("invalid repair transition %s -> %s", expected, next)
	}
	if err := validateRepairEvent(event, id); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE repair_cases SET state=$1, version=version+1, updated_at=$2
		WHERE id=$3 AND state=$4 AND version=$5`, next, event.CreatedAt.UTC(), id, expected, version)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if rows == 0 {
		return false, nil
	}
	if err := insertRepairEventTx(ctx, tx, event); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func validateRepairEvent(event coderepair.Event, caseID string) error {
	if event.ID == "" || event.CaseID != caseID || event.ActorID == "" || event.Type == "" || event.CreatedAt.IsZero() || !json.Valid([]byte(event.DetailsJSON)) {
		return errors.New("invalid repair audit event")
	}
	return nil
}

func insertRepairEventTx(ctx context.Context, tx *sql.Tx, event coderepair.Event) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO repair_events (id,case_id,actor_id,event_type,details_json,created_at)
		VALUES ($1,$2,$3,$4,$5::jsonb,$6)`, event.ID, event.CaseID, event.ActorID,
		event.Type, event.DetailsJSON, event.CreatedAt.UTC())
	return err
}

func (s *PostgresStore) ListRepairEvents(ctx context.Context, caseID string) ([]coderepair.Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,case_id,actor_id,event_type,details_json::text,created_at
		FROM repair_events WHERE case_id=$1 ORDER BY created_at,id`, caseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []coderepair.Event
	for rows.Next() {
		var event coderepair.Event
		if err := rows.Scan(&event.ID, &event.CaseID, &event.ActorID, &event.Type, &event.DetailsJSON, &event.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// ApproveRepairInvestigation makes the approval, state change, first attempt,
// audit event and durable job one transaction. A competing approver observes
// the new case version and cannot create a second attempt.
func (s *PostgresStore) ApproveRepairInvestigation(ctx context.Context, approval coderepair.Approval, attempt coderepair.Attempt, job coderepair.Job, event coderepair.Event) (bool, error) {
	if approval.ID == "" || approval.CaseID == "" || approval.CaseVersion < 1 || approval.Phase != "investigation" ||
		approval.Decision != "approved" || approval.ActorID == "" || approval.CreatedAt.IsZero() ||
		!approval.ExpiresAt.After(approval.CreatedAt) || approval.ExpiresAt.Sub(approval.CreatedAt) > 15*time.Minute ||
		attempt.ID == "" || attempt.CaseID != approval.CaseID ||
		attempt.Number < 1 || attempt.Status != coderepair.JobQueued || attempt.CreatedAt.IsZero() ||
		job.ID == "" || job.CaseID != approval.CaseID || job.AttemptID != attempt.ID ||
		job.Type != coderepair.JobTypeInvestigation || job.Status != coderepair.JobQueued ||
		job.MaxAttempts < 1 || job.DedupKey != coderepair.InvestigationDedupKey(approval.CaseID, attempt.Number) ||
		!json.Valid([]byte(job.PayloadJSON)) ||
		job.AvailableAt.IsZero() || job.CreatedAt.IsZero() {
		return false, errors.New("invalid investigation approval, attempt or job")
	}
	if err := validateRepairEvent(event, approval.CaseID); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	c, err := scanRepairCase(tx.QueryRowContext(ctx, `SELECT `+repairCaseColumns+` FROM repair_cases WHERE id=$1 FOR UPDATE`, approval.CaseID))
	if err != nil {
		return false, err
	}
	if c.State != coderepair.StateAwaitingInvestigationApproval || c.Version != approval.CaseVersion {
		return false, nil
	}
	var payload struct {
		CaseID          string `json:"case_id"`
		AttemptID       string `json:"attempt_id"`
		ExpectedVersion int64  `json:"expected_version"`
	}
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil ||
		payload.CaseID != c.ID || payload.AttemptID != attempt.ID || payload.ExpectedVersion != c.Version+1 {
		return false, errors.New("repair job payload does not match approved case version")
	}
	var previousAttempts int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM repair_attempts WHERE case_id=$1`, c.ID).Scan(&previousAttempts); err != nil {
		return false, err
	}
	if attempt.Number != previousAttempts+1 {
		return false, errors.New("repair attempt number is not the next sequence")
	}
	binding, err := getRepairBindingTx(ctx, tx, c.BindingID)
	if err != nil {
		return false, err
	}
	digest, err := coderepair.ScopeDigest(c, binding)
	if err != nil || !binding.Enabled || digest != c.ScopeDigest || approval.ScopeDigest != digest || approval.PolicyVersion != c.PolicyVersion {
		return false, errors.New("repair approval no longer matches repository, revision or policy")
	}
	var snapshotJSON, snapshotDigest string
	if err := tx.QueryRowContext(ctx, `SELECT snapshot_json::text,content_sha256 FROM repair_evidence
		WHERE case_id=$1 AND source='incident_snapshot'`, c.ID).Scan(&snapshotJSON, &snapshotDigest); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, errors.New("repair approval requires a captured evidence snapshot")
		}
		return false, err
	}
	var snapshot coderepair.EvidenceSnapshot
	if err := json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil || !snapshot.VerifyDigest() ||
		snapshot.SHA256 != snapshotDigest || snapshot.IncidentID != c.IncidentID ||
		snapshot.ServiceName != binding.ServiceName || snapshot.Environment != binding.Environment ||
		snapshot.DeployedRevision != c.DeployedSHA {
		return false, errors.New("repair approval evidence snapshot is corrupt or mismatched")
	}
	deployed, err := trustedDeployedRevisionTx(ctx, tx, c.IncidentID, binding.ServiceName, time.Now().UTC())
	if err != nil || deployed != c.DeployedSHA {
		return false, errors.New("repair approval requires fresh matching deployed revision")
	}
	freshLog, err := hasFreshRepairLogTx(ctx, tx, c.IncidentID, time.Now().UTC())
	if err != nil {
		return false, err
	}
	if !freshLog {
		return false, errors.New("repair approval requires fresh log evidence")
	}
	if !approval.ExpiresAt.After(time.Now().UTC()) {
		return false, errors.New("repair approval expired")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO repair_approvals
		(id,case_id,case_version,phase,actor_id,decision,scope_digest,policy_version,expires_at,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, approval.ID, approval.CaseID, approval.CaseVersion,
		approval.Phase, approval.ActorID, approval.Decision, approval.ScopeDigest, approval.PolicyVersion,
		approval.ExpiresAt.UTC(), approval.CreatedAt.UTC()); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO repair_attempts
		(id,case_id,attempt_number,status,provider,model,prompt_version,error_code,error_message,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'','',$8)`, attempt.ID, attempt.CaseID, attempt.Number, attempt.Status,
		attempt.Provider, attempt.Model, attempt.PromptVersion, attempt.CreatedAt.UTC()); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO repair_jobs
		(id,case_id,attempt_id,type,dedup_key,payload_json,status,attempts,max_attempts,available_at,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7,0,$8,$9,$10,$10)`, job.ID, job.CaseID, job.AttemptID,
		job.Type, job.DedupKey, job.PayloadJSON, job.Status, job.MaxAttempts, job.AvailableAt.UTC(), job.CreatedAt.UTC()); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE repair_cases SET state=$1, version=version+1, updated_at=$2
		WHERE id=$3 AND state=$4 AND version=$5`, coderepair.StateInvestigating, event.CreatedAt.UTC(),
		c.ID, c.State, c.Version)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if rows != 1 {
		return false, ErrRepairConflict
	}
	if err := insertRepairEventTx(ctx, tx, event); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *PostgresStore) AddRepairArtifact(ctx context.Context, artifact coderepair.Artifact, event coderepair.Event) error {
	if artifact.ID == "" || artifact.AttemptID == "" || artifact.Kind == "" || artifact.ArtifactRef == "" ||
		artifact.ByteSize < 0 || artifact.CreatedAt.IsZero() || len(artifact.ContentSHA256) != 64 {
		return errors.New("invalid repair artifact manifest")
	}
	if _, err := hex.DecodeString(artifact.ContentSHA256); err != nil {
		return errors.New("invalid repair artifact SHA-256")
	}
	if err := validateRepairEvent(event, event.CaseID); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var caseID string
	if err := tx.QueryRowContext(ctx, `SELECT case_id FROM repair_attempts WHERE id=$1`, artifact.AttemptID).Scan(&caseID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if event.CaseID != caseID {
		return errors.New("artifact event belongs to a different repair case")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO repair_artifacts
		(id,attempt_id,kind,content_sha256,artifact_ref,byte_size,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		artifact.ID, artifact.AttemptID, artifact.Kind, artifact.ContentSHA256, artifact.ArtifactRef,
		artifact.ByteSize, artifact.CreatedAt.UTC()); err != nil {
		return err
	}
	if err := insertRepairEventTx(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func redactRepairError(message string) string {
	message = strings.TrimSpace(security.Redact(message))
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}

func repairEvent(caseID, eventType, message string, now time.Time) coderepair.Event {
	return repairEventActor(caseID, "repair-runner", eventType, message, now)
}

func repairEventActor(caseID, actorID, eventType, message string, now time.Time) coderepair.Event {
	details, _ := json.Marshal(map[string]string{"reason": redactRepairError(message)})
	return coderepair.Event{ID: uuid.NewString(), CaseID: caseID, ActorID: actorID, Type: eventType,
		DetailsJSON: string(details), CreatedAt: now.UTC()}
}
