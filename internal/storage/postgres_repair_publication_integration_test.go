package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

func readyPublicationFixture(t *testing.T, store *PostgresStore) coderepair.Case {
	t.Helper()
	ctx := context.Background()
	c, job := claimedOutcomeFixture(t, store)
	patch := []byte("diff --git a/internal/workload/repair_fixture.go b/internal/workload/repair_fixture.go\nfixture patch\n")
	digest := sha256.Sum256(patch)
	patchSHA := hex.EncodeToString(digest[:])
	report, _ := json.Marshal(map[string]any{"status": coderepair.StatePatchReady, "recipe_id": "go-test-workload",
		"patch_sha256": patchSHA, "before_exit": 1, "after_exit": 0, "evidence_ids": []string{"log-1"}})
	outcome := coderepair.InvestigationOutcome{JobID: job.ID, LeaseToken: job.LeaseToken, CaseID: c.ID,
		AttemptID: job.AttemptID, ExpectedVersion: 3, State: coderepair.StatePatchReady,
		Patch: patch, PatchSHA256: patchSHA, ReportJSON: report, RecordedAt: time.Now().UTC()}
	if ok, err := store.RecordRepairInvestigationOutcome(ctx, outcome); err != nil || !ok { t.Fatalf("outcome: %v %v", ok, err) }
	if ok, err := store.CompleteRepairJob(ctx, job.ID, job.LeaseToken, time.Now().UTC()); err != nil || !ok { t.Fatalf("complete: %v %v", ok, err) }
	ready, err := store.GetRepairCase(ctx, c.ID)
	if err != nil || ready.State != coderepair.StatePatchReady { t.Fatalf("ready case: %+v %v", ready, err) }
	return ready
}

func TestRepairPRWebhookRejectsMismatchesAndReplayIntegration(t *testing.T) {
	store := isolatedRepairStore(t)
	ctx := context.Background()
	c := readyPublicationFixture(t, store)
	reviewDigest, ok, err := store.PrepareRepairPublication(ctx, c.ID, "operator", c.Version, time.Now().UTC())
	if err != nil || !ok { t.Fatalf("prepare: %v %v", ok, err) }
	c, _ = store.GetRepairCase(ctx, c.ID)
	_, ok, err = store.ApproveRepairPublication(ctx, c.ID, "operator", reviewDigest, c.Version, time.Now().UTC())
	if err != nil || !ok { t.Fatalf("approve: %v %v", ok, err) }
	p, err := store.ClaimRepairPublication(ctx, "publisher", time.Now().UTC(), time.Minute)
	if err != nil { t.Fatal(err) }
	head := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if ok, err := store.CompleteRepairPublication(ctx, p, head, 9, "https://github.com/acme/worker/pull/9", time.Now().UTC()); err != nil || !ok { t.Fatalf("complete: %v %v", ok, err) }
	base := RepairPREvent{DeliveryID: uuid.NewString(), Repository: "Cyaside/Triovexa", Branch: p.BranchName,
		BaseRef: "repair-code-workflow", HeadSHA: head, Number: 9, Merged: true,
		MergeSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", ReceivedAt: time.Now().UTC()}
	wrong := base
	wrong.DeliveryID = uuid.NewString()
	wrong.HeadSHA = "cccccccccccccccccccccccccccccccccccccccc"
	if applied, err := store.ApplyRepairPREvent(ctx, wrong); err != nil || applied { t.Fatalf("wrong head accepted: %v %v", applied, err) }
	wrong = base
	wrong.DeliveryID = uuid.NewString()
	wrong.Repository = "other/repo"
	if applied, err := store.ApplyRepairPREvent(ctx, wrong); err != nil || applied { t.Fatalf("wrong repo accepted: %v %v", applied, err) }
	if applied, err := store.ApplyRepairPREvent(ctx, base); err != nil || !applied { t.Fatalf("valid merge rejected: %v %v", applied, err) }
	if applied, err := store.ApplyRepairPREvent(ctx, base); err != nil || applied { t.Fatalf("replay mutated state: %v %v", applied, err) }
	c, err = store.GetRepairCase(ctx, c.ID)
	if err != nil || c.State != coderepair.StateMerged { t.Fatalf("merge state: %+v %v", c, err) }
}

func TestRepairPublicationApprovalAndLeaseSurviveRestartIntegration(t *testing.T) {
	store := isolatedRepairStore(t)
	ctx := context.Background()
	c := readyPublicationFixture(t, store)
	reviewDigest, ok, err := store.PrepareRepairPublication(ctx, c.ID, "operator", c.Version, time.Now().UTC())
	if err != nil || !ok { t.Fatalf("prepare: %v %v", ok, err) }
	c, _ = store.GetRepairCase(ctx, c.ID)
	if _, ok, err := store.ApproveRepairPublication(ctx, c.ID, "operator", "wrong-digest", c.Version, time.Now().UTC()); err == nil || ok {
		t.Fatalf("modified patch review accepted: %v %v", ok, err)
	}
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, approved, err := store.ApproveRepairPublication(ctx, c.ID, "operator", reviewDigest, c.Version, time.Now().UTC())
			if err != nil { t.Errorf("approve: %v", err) }
			results <- approved
		}()
	}
	wg.Wait()
	close(results)
	wins := 0
	for approved := range results { if approved { wins++ } }
	if wins != 1 { t.Fatalf("expected one approver, got %d", wins) }
	restarted := reopenRepairStore(t, store)
	p, err := restarted.ClaimRepairPublication(ctx, "publisher", time.Now().UTC(), time.Minute)
	if err != nil { t.Fatal(err) }
	input, err := restarted.GetRepairPublicationInput(ctx, p)
	if err != nil { t.Fatal(err) }
	if input.Approval.ScopeDigest != reviewDigest || input.Approval.ExpiresAt.Before(time.Now()) {
		t.Fatalf("approval did not survive restart: %+v", input.Approval)
	}
	if _, err := restarted.ClaimRepairPublication(ctx, "other", time.Now().UTC(), time.Minute); err != coderepair.ErrNoJobAvailable {
		t.Fatalf("live lease was stolen: %v", err)
	}
	if ok, err := restarted.CompleteRepairPublication(ctx, coderepair.Publication{ID: p.ID, CaseID: p.CaseID, LeaseToken: "stale"},
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 5, "https://github.com/acme/worker/pull/5", time.Now().UTC()); err != nil || ok {
		t.Fatalf("stale lease completed publication: %v %v", ok, err)
	}
	if ok, err := restarted.CompleteRepairPublication(ctx, p, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		5, "https://github.com/acme/worker/pull/5", time.Now().UTC()); err != nil || !ok {
		t.Fatalf("complete: %v %v", ok, err)
	}
	state, err := restarted.GetRepairCase(ctx, c.ID)
	if err != nil || state.State != coderepair.StatePROpen { t.Fatalf("published case: %+v %v", state, err) }
}
