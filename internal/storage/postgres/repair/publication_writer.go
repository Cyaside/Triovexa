package repair

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/google/uuid"
)

// Both reviewed and grant-authorized publication use this exact writer. The
// caller holds the case lock and has validated the binding and test proof.
func queuePublicationTx(ctx context.Context, tx *sql.Tx, c coderepair.Case, a coderepair.Attempt,
	actorID, phase, reviewDigest, patchDigest string, expires, now time.Time) (coderepair.Publication, error) {
	branch, err := coderepair.PublicationBranch(c.ID, a.Number)
	if err != nil {
		return coderepair.Publication{}, err
	}
	approvalID := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO repair_approvals
		(id,case_id,case_version,phase,actor_id,decision,scope_digest,policy_version,expires_at,created_at,patch_sha256)
		VALUES ($1,$2,$3,$4,$5,'approved',$6,$7,$8,$9,$10)`,
		approvalID, c.ID, c.Version, phase, actorID, reviewDigest, c.PolicyVersion, expires.UTC(), now.UTC(), patchDigest); err != nil {
		return coderepair.Publication{}, err
	}
	p := coderepair.Publication{ID: uuid.NewString(), CaseID: c.ID, AttemptID: a.ID, ApprovalID: approvalID,
		OperationID: fmt.Sprintf("repair-publish:%s:%d", c.ID, a.Number), BranchName: branch,
		PatchSHA256: patchDigest, State: coderepair.PublicationQueued, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	if _, err := tx.ExecContext(ctx, `INSERT INTO repair_publications
		(id,case_id,attempt_id,approval_id,operation_id,branch_name,patch_sha256,state,available_at,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, p.ID, p.CaseID, p.AttemptID, p.ApprovalID,
		p.OperationID, p.BranchName, p.PatchSHA256, p.State, now.UTC(), now.UTC(), now.UTC()); err != nil {
		return coderepair.Publication{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE repair_cases SET state='publishing',version=version+1,updated_at=$1 WHERE id=$2`, now.UTC(), c.ID); err != nil {
		return coderepair.Publication{}, err
	}
	details, _ := json.Marshal(map[string]string{"attempt_id": a.ID, "patch_sha256": patchDigest, "review_digest": reviewDigest, "operation_id": p.OperationID})
	eventType := "publication_approved"
	if phase == "automatic_publication" {
		eventType = "automatic_publication_authorized"
	}
	if err := insertRepairEventTx(ctx, tx, coderepair.Event{ID: uuid.NewString(), CaseID: c.ID, ActorID: actorID,
		Type: eventType, DetailsJSON: string(details), CreatedAt: now.UTC()}); err != nil {
		return coderepair.Publication{}, err
	}
	return p, nil
}
