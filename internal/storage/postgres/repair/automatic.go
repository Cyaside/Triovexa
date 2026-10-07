package repair

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

func (s *Store) HasAutomaticInvestigation(ctx context.Context, incidentID, bindingID string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM repair_automatic_dispatches WHERE incident_id=$1 AND binding_id=$2)`, incidentID, bindingID).Scan(&exists)
	return exists, err
}

// Case, immutable evidence, approval, attempt, job and dedup reservation commit
// together. The binding row serializes quota accounting across incidents.
func (s *Store) CreateAutomaticInvestigation(ctx context.Context, c coderepair.Case, event coderepair.Event, snapshot coderepair.EvidenceSnapshot, selection coderepair.AgentSelection) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	b, err := scanRepositoryBinding(tx.QueryRowContext(ctx, `SELECT `+repairBindingColumns+` FROM repository_bindings WHERE id=$1 FOR UPDATE`, c.BindingID))
	if err != nil {
		return false, err
	}
	now := time.Now().UTC()
	p := b.Automation
	if !b.Enabled || p == nil || !p.Enabled || p.Validate() != nil || !p.ExpiresAt.After(now) ||
		c.CreatedBy != p.AuthorizedBy || selection.Runtime == nil || selection.Runtime.CampaignID != p.CampaignID || selection.Runtime.MaxModelRequests != p.MaxModelRequests {
		return false, errors.New("automatic investigation grant no longer matches the proposed scope")
	}
	if selection.Runtime.Profile == "final-smoke" {
		return false, errors.New("final-smoke cannot authorize automatic investigations")
	}
	var admin bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND role='admin')`, p.AuthorizedBy).Scan(&admin); err != nil || !admin {
		return false, errors.New("automatic investigation grant has no active administrator")
	}
	if err = checkKillSwitch(ctx, tx); err != nil {
		return false, err
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM repair_automatic_dispatches WHERE incident_id=$1 AND binding_id=$2)`, c.IncidentID, b.ID).Scan(&exists); err != nil || exists {
		return false, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM repair_automatic_dispatches WHERE binding_id=$1`, b.ID).Scan(&count); err != nil {
		return false, err
	}
	if count >= p.MaxInvestigations {
		return false, errors.New("automatic investigation grant quota is exhausted")
	}
	if err = s.createRepairCaseTx(ctx, tx, c, event, &snapshot); err != nil {
		return false, err
	}
	approval, attempt, job, approvedEvent, err := coderepair.BuildInvestigationAuthorization(c, snapshot, p.AuthorizedBy, selection, now)
	if err != nil {
		return false, err
	}
	if p.ExpiresAt.Before(approval.ExpiresAt) {
		approval.ExpiresAt = p.ExpiresAt
	}
	approvedEvent.Type = "automatic_investigation_authorized"
	ok, err := s.approveRepairInvestigationTx(ctx, tx, approval, attempt, job, approvedEvent)
	if err != nil || !ok {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO repair_automatic_dispatches(incident_id,binding_id,case_id,created_at) VALUES($1,$2,$3,$4)`, c.IncidentID, b.ID, c.ID, now); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func checkKillSwitch(ctx context.Context, tx *sql.Tx) error {
	var value string
	err := tx.QueryRowContext(ctx, `SELECT value FROM application_settings WHERE key='safety.kill_switch'`).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	enabled, err := strconv.ParseBool(value)
	if err != nil || enabled {
		return errors.New("new repair work is disabled by the kill switch")
	}
	return nil
}

// Checked again by the model/tool fence: revocation must also stop queued and
// running investigations rather than only hiding an onboarding button.
func (s *Store) CheckRepairInvestigationSafety(ctx context.Context, caseID string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = checkKillSwitch(ctx, tx); err != nil {
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
	if err != nil || !b.Enabled || digest != c.ScopeDigest {
		return errors.New("approved repository scope was revoked or changed")
	}
	var automatic bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM repair_automatic_dispatches WHERE case_id=$1)`, caseID).Scan(&automatic); err != nil {
		return err
	}
	if automatic {
		p := b.Automation
		if p == nil || !p.Enabled || !p.ExpiresAt.After(now) {
			return errors.New("automatic investigation grant was revoked or expired")
		}
		var admin bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND role='admin')`, p.AuthorizedBy).Scan(&admin); err != nil || !admin {
			return errors.New("automatic investigation administrator is unavailable")
		}
	}
	return tx.Commit()
}
