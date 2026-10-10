package repair

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

// Runs in the job completion/recovery transaction. A verified patch survives
// grant revocation, but revocation cannot authorize a new external write.
func queueAutomaticPublicationTx(ctx context.Context, tx *sql.Tx, caseID string, now time.Time) error {
	c, err := scanRepairCase(tx.QueryRowContext(ctx, `SELECT `+repairCaseColumns+` FROM repair_cases WHERE id=$1 FOR UPDATE`, caseID))
	if err != nil || c.State != coderepair.StatePatchReady {
		return err
	}
	b, err := getRepairBindingTx(ctx, tx, c.BindingID)
	if err != nil {
		return err
	}
	if b.Automation == nil || !b.Automation.PublishDraftPR {
		return nil
	}
	if err := checkAutomaticPublicationGrant(ctx, tx, c, b, now); err != nil {
		return insertRepairEventTx(ctx, tx, repairEvent(c.ID, "automatic_publication_blocked", err.Error(), now))
	}
	a, patchDigest, err := latestVerifiedRepairPatchTx(ctx, tx, c.ID)
	if err != nil {
		return err
	}
	reviewDigest, err := coderepair.PublicationDigest(c, a, patchDigest)
	if err != nil {
		return err
	}
	expires := now.Add(15 * time.Minute)
	if b.Automation.ExpiresAt.Before(expires) {
		expires = b.Automation.ExpiresAt
	}
	_, err = queuePublicationTx(ctx, tx, c, a, b.Automation.AuthorizedBy, "automatic_publication", reviewDigest, patchDigest, expires, now)
	return err
}

func checkAutomaticPublicationGrant(ctx context.Context, tx *sql.Tx, c coderepair.Case, b coderepair.RepositoryBinding, now time.Time) error {
	p := b.Automation
	digest, err := coderepair.ScopeDigest(c, b)
	if err != nil || !b.Enabled || digest != c.ScopeDigest || p == nil || !p.Enabled || !p.PublishDraftPR ||
		p.Validate() != nil || !p.ExpiresAt.After(now) || p.AuthorizedBy != c.CreatedBy {
		return errors.New("automatic publication grant was revoked, changed or expired")
	}
	var authorized bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM repair_automatic_dispatches d
		JOIN users u ON u.id=$2 AND u.role='admin' WHERE d.case_id=$1 AND d.binding_id=$3)`, c.ID, p.AuthorizedBy, b.ID).Scan(&authorized); err != nil {
		return err
	}
	if !authorized {
		return errors.New("automatic publication requires the original active administrator and alert grant")
	}
	return checkKillSwitch(ctx, tx)
}

// The dedicated publisher calls this again immediately before GitHub writes.
// Existing remote effects can still be reconciled without authorizing new ones.
func (s *Store) CheckRepairPublicationSafety(ctx context.Context, caseID string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := checkKillSwitch(ctx, tx); err != nil {
		return err
	}
	c, err := scanRepairCase(tx.QueryRowContext(ctx, `SELECT `+repairCaseColumns+` FROM repair_cases WHERE id=$1`, caseID))
	if err != nil {
		return err
	}
	b, err := getRepairBindingTx(ctx, tx, c.BindingID)
	if err != nil {
		return err
	}
	digest, err := coderepair.ScopeDigest(c, b)
	if err != nil || !b.Enabled || digest != c.ScopeDigest || c.State != coderepair.StatePublishing {
		return errors.New("publication repository scope or state changed")
	}
	var phase string
	if err := tx.QueryRowContext(ctx, `SELECT a.phase FROM repair_publications p JOIN repair_approvals a ON a.id=p.approval_id WHERE p.case_id=$1`, caseID).Scan(&phase); err != nil {
		return err
	}
	if phase == "automatic_publication" {
		if err := checkAutomaticPublicationGrant(ctx, tx, c, b, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}
