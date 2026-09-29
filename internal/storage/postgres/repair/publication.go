package repair

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

const publicationColumns = `id,case_id,attempt_id,approval_id,operation_id,branch_name,
	patch_sha256,head_sha,merge_sha,pr_number,pr_url,state,lease_owner,lease_token,lease_until,
	attempts,failures,last_error,created_at,updated_at`

func scanPublication(row repairScanner) (coderepair.Publication, error) {
	var publication coderepair.Publication
	var approvalID sql.NullString
	var prNumber sql.NullInt64
	var leaseUntil sql.NullTime
	err := row.Scan(&publication.ID, &publication.CaseID, &publication.AttemptID,
		&approvalID, &publication.OperationID, &publication.BranchName,
		&publication.PatchSHA256, &publication.HeadSHA, &publication.MergeSHA, &prNumber, &publication.PRURL,
		&publication.State, &publication.LeaseOwner, &publication.LeaseToken, &leaseUntil,
		&publication.Attempts, &publication.Failures, &publication.LastError, &publication.CreatedAt, &publication.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return coderepair.Publication{}, ErrNotFound
	}
	if approvalID.Valid {
		publication.ApprovalID = approvalID.String
	}
	if prNumber.Valid {
		publication.PRNumber = prNumber.Int64
	}
	if leaseUntil.Valid {
		publication.LeaseUntil = leaseUntil.Time
	}
	return publication, err
}

func (s *Store) GetRepairPublication(ctx context.Context, caseID string) (coderepair.Publication, error) {
	return scanPublication(s.db.QueryRowContext(ctx, `SELECT `+publicationColumns+` FROM repair_publications WHERE case_id=$1`, caseID))
}

func latestVerifiedRepairPatchTx(ctx context.Context, tx *sql.Tx, caseID string) (coderepair.Attempt, string, error) {
	var a coderepair.Attempt
	var digest string
	var patch, report []byte
	err := tx.QueryRowContext(ctx, `SELECT a.id,a.case_id,a.attempt_number,a.status,
		p.content_sha256,p.content,r.content FROM repair_attempts a
		JOIN repair_artifacts p ON p.attempt_id=a.id AND p.kind='patch' AND p.artifact_ref='database'
		JOIN repair_artifacts r ON r.attempt_id=a.id AND r.kind='investigation_report' AND r.artifact_ref='database'
		WHERE a.case_id=$1 ORDER BY a.attempt_number DESC LIMIT 1`, caseID).
		Scan(&a.ID, &a.CaseID, &a.Number, &a.Status, &digest, &patch, &report)
	if err != nil {
		return coderepair.Attempt{}, "", err
	}
	if a.Status != "succeeded" || len(patch) == 0 || len(report) == 0 {
		return coderepair.Attempt{}, "", errors.New("publication requires a completed verified investigation")
	}
	actual := sha256.Sum256(patch)
	if digest != hex.EncodeToString(actual[:]) {
		return coderepair.Attempt{}, "", errors.New("publication patch digest mismatch")
	}
	var proof struct {
		Status      coderepair.State `json:"status"`
		PatchSHA256 string           `json:"patch_sha256"`
		BeforeExit  int              `json:"before_exit"`
		AfterExit   int              `json:"after_exit"`
		EvidenceIDs []string         `json:"evidence_ids"`
	}
	if err := json.Unmarshal(report, &proof); err != nil || proof.Status != coderepair.StatePatchReady ||
		proof.PatchSHA256 != digest || proof.BeforeExit == 0 || proof.AfterExit != 0 || len(proof.EvidenceIDs) == 0 {
		return coderepair.Attempt{}, "", errors.New("publication has no matching red-to-green proof")
	}
	return a, digest, nil
}

// PrepareRepairPublication exposes an immutable, already-tested patch for
// review. This does not authorize or perform any GitHub write.
func (s *Store) PrepareRepairPublication(ctx context.Context, caseID, actorID string, expectedVersion int64, now time.Time) (string, bool, error) {
	if caseID == "" || actorID == "" || expectedVersion < 1 || now.IsZero() {
		return "", false, errors.New("invalid publication review request")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	c, err := scanRepairCase(tx.QueryRowContext(ctx, `SELECT `+repairCaseColumns+` FROM repair_cases WHERE id=$1 FOR UPDATE`, caseID))
	if err != nil {
		return "", false, err
	}
	if c.State != coderepair.StatePatchReady || c.Version != expectedVersion {
		return "", false, nil
	}
	a, digest, err := latestVerifiedRepairPatchTx(ctx, tx, caseID)
	if err != nil {
		return "", false, err
	}
	binding, err := getRepairBindingTx(ctx, tx, c.BindingID)
	if err != nil {
		return "", false, err
	}
	scopeDigest, err := coderepair.ScopeDigest(c, binding)
	if err != nil || !binding.Enabled || scopeDigest != c.ScopeDigest {
		return "", false, errors.New("repository scope changed before publication review")
	}
	reviewDigest, err := coderepair.PublicationDigest(c, a, digest)
	if err != nil {
		return "", false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE repair_cases SET state='awaiting_publish_approval',version=version+1,updated_at=$1 WHERE id=$2`, now.UTC(), caseID); err != nil {
		return "", false, err
	}
	details, _ := json.Marshal(map[string]string{"attempt_id": a.ID, "patch_sha256": digest, "review_digest": reviewDigest})
	if err := insertRepairEventTx(ctx, tx, coderepair.Event{ID: uuid.NewString(), CaseID: caseID, ActorID: actorID,
		Type: "publication_review_requested", DetailsJSON: string(details), CreatedAt: now.UTC()}); err != nil {
		return "", false, err
	}
	return reviewDigest, true, tx.Commit()
}

// ApproveRepairPublication atomically records a short-lived approval, state
// transition, durable publication operation and audit event. Concurrent
// approvers cannot both win the locked case version.
func (s *Store) ApproveRepairPublication(ctx context.Context, caseID, actorID, reviewDigest string, expectedVersion int64, now time.Time) (coderepair.Publication, bool, error) {
	if caseID == "" || actorID == "" || reviewDigest == "" || expectedVersion < 1 || now.IsZero() {
		return coderepair.Publication{}, false, errors.New("invalid publication approval")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return coderepair.Publication{}, false, err
	}
	defer tx.Rollback()
	c, err := scanRepairCase(tx.QueryRowContext(ctx, `SELECT `+repairCaseColumns+` FROM repair_cases WHERE id=$1 FOR UPDATE`, caseID))
	if err != nil {
		return coderepair.Publication{}, false, err
	}
	if c.State != coderepair.StateAwaitingPublishApproval || c.Version != expectedVersion {
		return coderepair.Publication{}, false, nil
	}
	var users int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&users); err != nil {
		return coderepair.Publication{}, false, err
	}
	if users > 1 && c.CreatedBy == actorID {
		return coderepair.Publication{}, false, errors.New("publication requires an independent operator approval")
	}
	a, patchDigest, err := latestVerifiedRepairPatchTx(ctx, tx, caseID)
	if err != nil {
		return coderepair.Publication{}, false, err
	}
	binding, err := getRepairBindingTx(ctx, tx, c.BindingID)
	if err != nil {
		return coderepair.Publication{}, false, err
	}
	scopeDigest, err := coderepair.ScopeDigest(c, binding)
	if err != nil || !binding.Enabled || scopeDigest != c.ScopeDigest {
		return coderepair.Publication{}, false, errors.New("publication repository scope changed")
	}
	actualReviewDigest, err := coderepair.PublicationDigest(c, a, patchDigest)
	if err != nil || actualReviewDigest != reviewDigest {
		return coderepair.Publication{}, false, errors.New("reviewed patch or policy changed")
	}
	branch, err := coderepair.PublicationBranch(c.ID, a.Number)
	if err != nil {
		return coderepair.Publication{}, false, err
	}
	approvalID := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO repair_approvals
		(id,case_id,case_version,phase,actor_id,decision,scope_digest,policy_version,expires_at,created_at,patch_sha256)
		VALUES ($1,$2,$3,'publication',$4,'approved',$5,$6,$7,$8,$9)`,
		approvalID, caseID, c.Version, actorID, reviewDigest, c.PolicyVersion, now.Add(15*time.Minute).UTC(), now.UTC(), patchDigest); err != nil {
		return coderepair.Publication{}, false, err
	}
	p := coderepair.Publication{ID: uuid.NewString(), CaseID: caseID, AttemptID: a.ID, ApprovalID: approvalID,
		OperationID: fmt.Sprintf("repair-publish:%s:%d", caseID, a.Number), BranchName: branch,
		PatchSHA256: patchDigest, State: coderepair.PublicationQueued, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	if _, err := tx.ExecContext(ctx, `INSERT INTO repair_publications
		(id,case_id,attempt_id,approval_id,operation_id,branch_name,patch_sha256,state,available_at,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, p.ID, p.CaseID, p.AttemptID, p.ApprovalID,
		p.OperationID, p.BranchName, p.PatchSHA256, p.State, now.UTC(), now.UTC(), now.UTC()); err != nil {
		return coderepair.Publication{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE repair_cases SET state='publishing',version=version+1,updated_at=$1 WHERE id=$2`, now.UTC(), caseID); err != nil {
		return coderepair.Publication{}, false, err
	}
	details, _ := json.Marshal(map[string]string{"attempt_id": a.ID, "patch_sha256": patchDigest, "review_digest": reviewDigest, "operation_id": p.OperationID})
	if err := insertRepairEventTx(ctx, tx, coderepair.Event{ID: uuid.NewString(), CaseID: caseID, ActorID: actorID,
		Type: "publication_approved", DetailsJSON: string(details), CreatedAt: now.UTC()}); err != nil {
		return coderepair.Publication{}, false, err
	}
	return p, true, tx.Commit()
}

// ClaimRepairPublication leases one operation. A recovered lease is fenced by
// a new token, while the GitHub adapter reconciles any uncertain remote effect.
func (s *Store) ClaimRepairPublication(ctx context.Context, worker string, now time.Time, lease time.Duration) (coderepair.Publication, error) {
	if worker == "" || now.IsZero() || lease <= 0 {
		return coderepair.Publication{}, errors.New("invalid publication lease")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return coderepair.Publication{}, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT p.id FROM repair_publications p JOIN repair_cases c ON c.id=p.case_id
		WHERE c.state='publishing' AND p.available_at<=$1 AND
		(p.state='queued' OR (p.state='running' AND p.lease_until<=$1))
		ORDER BY p.created_at FOR UPDATE OF p SKIP LOCKED LIMIT 1`, now.UTC()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return coderepair.Publication{}, coderepair.ErrNoJobAvailable
	}
	if err != nil {
		return coderepair.Publication{}, err
	}
	token := uuid.NewString()
	p, err := scanPublication(tx.QueryRowContext(ctx, `UPDATE repair_publications SET state='running',
		lease_owner=$1,lease_token=$2,lease_until=$3,attempts=attempts+1,updated_at=$4 WHERE id=$5
		RETURNING `+publicationColumns, worker, token, now.Add(lease).UTC(), now.UTC(), id))
	if err != nil {
		return coderepair.Publication{}, err
	}
	return p, tx.Commit()
}

type PublicationInput struct {
	Case       coderepair.Case
	Attempt    coderepair.Attempt
	Binding    coderepair.RepositoryBinding
	Approval   coderepair.Approval
	Patch      []byte
	ReportJSON []byte
}

func (s *Store) GetRepairPublicationInput(ctx context.Context, publication coderepair.Publication) (PublicationInput, error) {
	var input PublicationInput
	var err error
	input.Case, err = s.GetRepairCase(ctx, publication.CaseID)
	if err != nil {
		return input, err
	}
	input.Attempt, err = s.GetRepairAttempt(ctx, publication.AttemptID)
	if err != nil {
		return input, err
	}
	input.Binding, err = s.GetRepositoryBinding(ctx, input.Case.BindingID)
	if err != nil {
		return input, err
	}
	input.Patch, err = s.GetRepairArtifactContent(ctx, publication.AttemptID, "patch")
	if err != nil {
		return input, err
	}
	input.ReportJSON, err = s.GetRepairArtifactContent(ctx, publication.AttemptID, "investigation_report")
	if err != nil {
		return input, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT id,case_id,case_version,phase,actor_id,decision,scope_digest,
		policy_version,expires_at,created_at FROM repair_approvals WHERE id=$1`, publication.ApprovalID).
		Scan(&input.Approval.ID, &input.Approval.CaseID, &input.Approval.CaseVersion,
			&input.Approval.Phase, &input.Approval.ActorID, &input.Approval.Decision,
			&input.Approval.ScopeDigest, &input.Approval.PolicyVersion,
			&input.Approval.ExpiresAt, &input.Approval.CreatedAt)
	if err != nil {
		return input, err
	}
	return input, nil
}

func (s *Store) CompleteRepairPublication(ctx context.Context, p coderepair.Publication, headSHA string, number int64, prURL string, now time.Time) (bool, error) {
	if p.ID == "" || p.LeaseToken == "" || !coderepair.ValidGitRevision(headSHA) || number < 1 || prURL == "" || now.IsZero() {
		return false, errors.New("invalid published PR result")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	current, err := scanPublication(tx.QueryRowContext(ctx, `SELECT `+publicationColumns+` FROM repair_publications WHERE id=$1 FOR UPDATE`, p.ID))
	if err != nil {
		return false, err
	}
	if current.LeaseToken != p.LeaseToken || current.State != coderepair.PublicationRunning || !current.LeaseUntil.After(now) {
		return false, nil
	}
	c, err := scanRepairCase(tx.QueryRowContext(ctx, `SELECT `+repairCaseColumns+` FROM repair_cases WHERE id=$1 FOR UPDATE`, p.CaseID))
	if err != nil {
		return false, err
	}
	if c.State != coderepair.StatePublishing {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE repair_publications SET state='pr_open',head_sha=$1,pr_number=$2,
		pr_url=$3,lease_owner='',lease_token='',lease_until=NULL,updated_at=$4 WHERE id=$5`,
		headSHA, number, prURL, now.UTC(), p.ID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE repair_cases SET state='pr_open',version=version+1,updated_at=$1 WHERE id=$2`, now.UTC(), p.CaseID); err != nil {
		return false, err
	}
	details, _ := json.Marshal(map[string]any{"pr_number": number, "head_sha": headSHA, "operation_id": p.OperationID})
	if err := insertRepairEventTx(ctx, tx, coderepair.Event{ID: uuid.NewString(), CaseID: p.CaseID, ActorID: "repair-publisher",
		Type: "pull_request_opened", DetailsJSON: string(details), CreatedAt: now.UTC()}); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *Store) BlockRepairPublication(ctx context.Context, p coderepair.Publication, reason string, now time.Time) (bool, error) {
	if p.ID == "" || p.LeaseToken == "" || now.IsZero() {
		return false, errors.New("invalid publication failure")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	current, err := scanPublication(tx.QueryRowContext(ctx, `SELECT `+publicationColumns+` FROM repair_publications WHERE id=$1 FOR UPDATE`, p.ID))
	if err != nil {
		return false, err
	}
	if current.State != coderepair.PublicationRunning || current.LeaseToken != p.LeaseToken || !current.LeaseUntil.After(now) {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE repair_publications SET state='blocked',last_error=$1,
		lease_owner='',lease_token='',lease_until=NULL,updated_at=$2 WHERE id=$3`, redactRepairError(reason), now.UTC(), p.ID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE repair_cases SET state='blocked',version=version+1,updated_at=$1 WHERE id=$2 AND state='publishing'`, now.UTC(), p.CaseID); err != nil {
		return false, err
	}
	if err := insertRepairEventTx(ctx, tx, repairEventActor(p.CaseID, "repair-publisher", "publication_blocked", reason, now)); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// FailRepairPublication bounds explicit failures. A process crash does not
// consume this budget: its expired lease is reconciled on the next claim.
func (s *Store) FailRepairPublication(ctx context.Context, p coderepair.Publication, reason string, now time.Time) (bool, error) {
	if p.ID == "" || p.LeaseToken == "" || now.IsZero() {
		return false, errors.New("invalid publication failure")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	current, err := scanPublication(tx.QueryRowContext(ctx, `SELECT `+publicationColumns+` FROM repair_publications WHERE id=$1 FOR UPDATE`, p.ID))
	if err != nil {
		return false, err
	}
	if current.State != coderepair.PublicationRunning || current.LeaseToken != p.LeaseToken || !current.LeaseUntil.After(now) {
		return false, nil
	}
	if current.Failures >= 2 {
		if _, err := tx.ExecContext(ctx, `UPDATE repair_publications SET state='blocked',failures=failures+1,
			last_error=$1,lease_owner='',lease_token='',lease_until=NULL,updated_at=$2 WHERE id=$3`,
			redactRepairError(reason), now.UTC(), p.ID); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE repair_cases SET state='blocked',version=version+1,updated_at=$1
			WHERE id=$2 AND state='publishing'`, now.UTC(), p.CaseID); err != nil {
			return false, err
		}
		if err := insertRepairEventTx(ctx, tx, repairEventActor(p.CaseID, "repair-publisher", "publication_blocked", reason, now)); err != nil {
			return false, err
		}
	} else {
		delay := time.Duration((current.Failures+1)*(current.Failures+1)) * time.Second
		if _, err := tx.ExecContext(ctx, `UPDATE repair_publications SET state='queued',failures=failures+1,
			available_at=$1,last_error=$2,lease_owner='',lease_token='',lease_until=NULL,updated_at=$3 WHERE id=$4`,
			now.Add(delay).UTC(), redactRepairError(reason), now.UTC(), p.ID); err != nil {
			return false, err
		}
		if err := insertRepairEventTx(ctx, tx, repairEventActor(p.CaseID, "repair-publisher", "publication_retry_scheduled", reason, now)); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}
