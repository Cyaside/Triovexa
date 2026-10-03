package repair

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

func receiptFence(ctx context.Context, tx *sql.Tx, claim coderepair.ToolReceiptClaim) error {
	var valid bool
	err := tx.QueryRowContext(ctx, `SELECT j.lease_until>clock_timestamp() FROM repair_jobs j JOIN repair_cases c ON c.id=j.case_id
		WHERE j.id=$1 AND j.attempt_id=$2 AND j.case_id=$3 AND j.lease_token=$4 AND j.status='running'
		AND c.state='investigating' AND c.version=$5 FOR UPDATE OF j,c`, claim.Job.ID, claim.Job.AttemptID,
		claim.Job.CaseID, claim.Job.LeaseToken, claim.ExpectedVersion).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return errors.New("repair tool receipt lease lost")
	}
	return nil
}

func scanToolReceipt(row repairScanner) (coderepair.ToolReceipt, error) {
	var r coderepair.ToolReceipt
	var result sql.NullString
	var completed sql.NullTime
	err := row.Scan(&r.AttemptID, &r.CallID, &r.Name, &r.ArgsSHA256, &r.Revision, &r.State, &result, &r.CreatedAt, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return r, coderepair.ErrToolReceiptMissing
	}
	r.ResultSealed, r.CompletedAt = result.String, completed.Time
	return r, err
}

const toolReceiptColumns = `attempt_id,call_id,name,args_sha256,revision,state,result_sealed,created_at,completed_at`

func (s *Store) StartRepairTool(ctx context.Context, claim coderepair.ToolReceiptClaim, r coderepair.ToolReceipt) (coderepair.ToolReceipt, error) {
	if r.AttemptID != claim.Job.AttemptID || r.CallID == "" || len(r.CallID) > 128 || r.Name == "" || len(r.Name) > 64 ||
		len(r.ArgsSHA256) != 64 || !coderepair.ValidGitRevision(r.Revision) || r.State != "started" {
		return r, coderepair.ErrToolReceiptMismatch
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	if err = receiptFence(ctx, tx, claim); err != nil {
		return r, err
	}
	inserted, err := tx.ExecContext(ctx, `INSERT INTO repair_tool_receipts(attempt_id,call_id,name,args_sha256,revision,state,created_at)
		VALUES($1,$2,$3,$4,$5,'started',$6) ON CONFLICT(attempt_id,call_id) DO NOTHING`, r.AttemptID, r.CallID, r.Name, r.ArgsSHA256, r.Revision, time.Now().UTC())
	if err != nil {
		return r, err
	}
	newRows, err := inserted.RowsAffected()
	if err != nil {
		return r, err
	}
	stored, err := scanToolReceipt(tx.QueryRowContext(ctx, `SELECT `+toolReceiptColumns+` FROM repair_tool_receipts WHERE attempt_id=$1 AND call_id=$2`, r.AttemptID, r.CallID))
	if err != nil {
		return r, err
	}
	if stored.Name != r.Name || stored.ArgsSHA256 != r.ArgsSHA256 || stored.Revision != r.Revision {
		return r, coderepair.ErrToolReceiptMismatch
	}
	if newRows == 0 && stored.State == "started" {
		return r, errors.New("TOOL_DISPATCH_UNCERTAIN")
	}
	return stored, tx.Commit()
}

func (s *Store) CompleteRepairTool(ctx context.Context, claim coderepair.ToolReceiptClaim, r coderepair.ToolReceipt) error {
	if r.AttemptID != claim.Job.AttemptID || !strings.HasPrefix(r.ResultSealed, "v1.") || len(r.ResultSealed) > 350000 {
		return coderepair.ErrToolReceiptMismatch
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = receiptFence(ctx, tx, claim); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE repair_tool_receipts SET state='completed',result_sealed=$1,completed_at=now()
		WHERE attempt_id=$2 AND call_id=$3 AND name=$4 AND args_sha256=$5 AND revision=$6 AND state='started'`, r.ResultSealed, r.AttemptID, r.CallID, r.Name, r.ArgsSHA256, r.Revision)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return coderepair.ErrToolReceiptMismatch
	}
	return tx.Commit()
}

func (s *Store) ListRepairToolReceipts(ctx context.Context, claim coderepair.ToolReceiptClaim) ([]coderepair.ToolReceipt, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = receiptFence(ctx, tx, claim); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+toolReceiptColumns+` FROM repair_tool_receipts WHERE attempt_id=$1 ORDER BY receipt_order LIMIT 101`, claim.Job.AttemptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var receipts []coderepair.ToolReceipt
	for rows.Next() {
		r, err := scanToolReceipt(rows)
		if err != nil {
			return nil, err
		}
		receipts = append(receipts, r)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(receipts) > 100 {
		return nil, errors.New("repair tool receipt limit exceeded")
	}
	rows.Close()
	return receipts, tx.Commit()
}
