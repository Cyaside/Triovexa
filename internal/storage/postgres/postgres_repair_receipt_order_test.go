package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

func TestRepairReceiptRecoveryIgnoresWallClockAndCallIDOrderIntegration(t *testing.T) {
	store := isolatedRepairStore(t)
	ctx := context.Background()
	c, approval, attempt, job := makeInvestigationTestCase(t, store, 2)
	if ok, err := store.ApproveRepairInvestigation(ctx, approval, attempt, job, repairTestEvent(c.ID, "investigation_approved", time.Now().UTC())); err != nil || !ok {
		t.Fatalf("approve: %v %v", ok, err)
	}
	claimed, err := store.ClaimRepairJob(ctx, "fixture-worker", time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	claim := coderepair.ToolReceiptClaim{Job: claimed, ExpectedVersion: c.Version + 1}
	for _, id := range []string{"z-first", "a-second"} {
		r := coderepair.ToolReceipt{AttemptID: attempt.ID, CallID: id, Name: "repo_read", ArgsSHA256: strings.Repeat("c", 64), Revision: c.BaseSHA, State: "started"}
		if _, err := store.StartRepairTool(ctx, claim, r); err != nil {
			t.Fatal(err)
		}
		r.ResultSealed = "v1.synthetic-receipt"
		if err := store.CompleteRepairTool(ctx, claim, r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE repair_tool_receipts SET created_at=$1 WHERE attempt_id=$2`, time.Unix(1, 0).UTC(), attempt.ID); err != nil {
		t.Fatal(err)
	}
	receipts, err := store.ListRepairToolReceipts(ctx, claim)
	if err != nil || len(receipts) != 2 || receipts[0].CallID != "z-first" || receipts[1].CallID != "a-second" {
		t.Fatalf("durable dispatch order changed: receipts=%+v err=%v", receipts, err)
	}
}
