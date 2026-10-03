package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

func TestRepairRuntimeApprovalAndToolReceiptsAreDurableIntegration(t *testing.T) {
	store := isolatedRepairStore(t)
	ctx := context.Background()
	c, approval, attempt, job := makeInvestigationTestCase(t, store, 2)
	attempt.Runtime = &coderepair.RuntimeSpec{EngineID: "deepagents", EngineVersion: "1.14.1", ContractVersion: "1", CheckpointVersion: "1", ThreadID: c.ID + ":" + attempt.ID, Profile: "offline-fixture", ConfigVersion: "fixture-v1", CampaignID: "fixture-campaign", PlaybookDigest: strings.Repeat("a", 64), SnapshotSHA256: strings.Repeat("b", 64), SealedSnapshot: "v1.sealed-fixture"}
	if ok, err := store.ApproveRepairInvestigation(ctx, approval, attempt, job, repairTestEvent(c.ID, "investigation_approved", time.Now().UTC())); err != nil || !ok {
		t.Fatalf("approve: %v %v", ok, err)
	}
	loaded, err := store.GetRepairAttempt(ctx, attempt.ID)
	if err != nil || loaded.Runtime == nil || *loaded.Runtime != *attempt.Runtime {
		t.Fatal("runtime snapshot not atomic/durable", err)
	}
	public, _ := json.Marshal(loaded)
	if strings.Contains(string(public), "sealed-fixture") {
		t.Fatal("public attempt leaked runtime configuration")
	}
	claimed, err := store.ClaimRepairJob(ctx, "fixture-worker", time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	claim := coderepair.ToolReceiptClaim{Job: claimed, ExpectedVersion: c.Version + 1}
	r := coderepair.ToolReceipt{AttemptID: attempt.ID, CallID: "read-1", Name: "repo_read", ArgsSHA256: strings.Repeat("c", 64), Revision: c.BaseSHA, State: "started"}
	started, err := store.StartRepairTool(ctx, claim, r)
	if err != nil || started.State != "started" {
		t.Fatal("start", err)
	}
	if _, err := store.StartRepairTool(ctx, claim, r); err == nil {
		t.Fatal("two dispatchers claimed the same started tool effect")
	}
	r.ResultSealed = "v1.result-fixture"
	if err := store.CompleteRepairTool(ctx, claim, r); err != nil {
		t.Fatal(err)
	}
	resumed, err := store.StartRepairTool(ctx, claim, r)
	if err != nil || resumed.State != "completed" || resumed.ResultSealed != r.ResultSealed {
		t.Fatal("receipt lost on replay", err)
	}
	changed := r
	changed.ArgsSHA256 = strings.Repeat("d", 64)
	if _, err := store.StartRepairTool(ctx, claim, changed); err == nil {
		t.Fatal("same call ID accepted changed arguments")
	}
	stale := claim
	stale.Job.LeaseToken = "other-fence"
	if _, err := store.ListRepairToolReceipts(ctx, stale); err == nil {
		t.Fatal("stale process read tool results")
	}
	if err := store.CompleteRepairTool(ctx, stale, r); err == nil {
		t.Fatal("stale process wrote tool results")
	}
}
