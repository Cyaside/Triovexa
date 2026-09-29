package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

func claimedOutcomeFixture(t *testing.T, store *PostgresStore) (coderepair.Case, coderepair.Job) {
	t.Helper()
	ctx := context.Background()
	c, approval, attempt, job := makeInvestigationTestCase(t, store, 2)
	if ok, err := store.ApproveRepairInvestigation(ctx, approval, attempt, job,
		repairTestEvent(c.ID, "investigation_approved", time.Now().UTC())); err != nil || !ok {
		t.Fatalf("approve investigation: ok=%v err=%v", ok, err)
	}
	claimed, err := store.ClaimRepairJob(ctx, "worker-one", time.Now().UTC(), 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return c, claimed
}

func TestRepairInvestigationOutcomePersistsRedGreenPatchIntegration(t *testing.T) {
	store := isolatedRepairStore(t)
	ctx := context.Background()
	c, job := claimedOutcomeFixture(t, store)
	patch := []byte("diff --git a/internal/workload/repair_fixture.go b/internal/workload/repair_fixture.go\nfixture patch\n")
	digest := sha256.Sum256(patch)
	patchSHA := hex.EncodeToString(digest[:])
	report, _ := json.Marshal(map[string]any{
		"status": coderepair.StatePatchReady, "recipe_id": "go-test-workload", "patch_sha256": patchSHA,
		"before_exit": 1, "after_exit": 0, "evidence_ids": []string{"log-1"},
	})
	outcome := coderepair.InvestigationOutcome{JobID: job.ID, LeaseToken: job.LeaseToken, CaseID: c.ID,
		AttemptID: job.AttemptID, ExpectedVersion: 3, State: coderepair.StatePatchReady,
		Patch: patch, PatchSHA256: patchSHA, ReportJSON: report, RecordedAt: time.Now().UTC()}
	stale := outcome
	stale.LeaseToken = "stale-worker-token"
	if ok, err := store.RecordRepairInvestigationOutcome(ctx, stale); err != nil || ok {
		t.Fatalf("stale worker recorded a patch: ok=%v err=%v", ok, err)
	}
	if ok, err := store.RecordRepairInvestigationOutcome(ctx, outcome); err != nil || !ok {
		t.Fatalf("record verified outcome: ok=%v err=%v", ok, err)
	}
	if ok, err := store.RecordRepairInvestigationOutcome(ctx, outcome); err != nil || ok {
		t.Fatalf("duplicate or stale outcome accepted: ok=%v err=%v", ok, err)
	}
	restarted := reopenRepairStore(t, store)
	got, err := restarted.GetRepairArtifactContent(ctx, job.AttemptID, "patch")
	if err != nil || !bytes.Equal(got, patch) {
		t.Fatalf("patch did not survive restart: %v, %v", got, err)
	}
	if _, err := restarted.GetRepairArtifactContent(ctx, job.AttemptID, "investigation_report"); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.db.ExecContext(ctx, `UPDATE repair_artifacts SET content=$1
		WHERE attempt_id=$2 AND kind='patch'`, []byte("tampered"), job.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.GetRepairArtifactContent(ctx, job.AttemptID, "patch"); err == nil {
		t.Fatal("tampered patch content passed digest verification")
	}
	if ok, err := restarted.CompleteRepairJob(ctx, job.ID, job.LeaseToken, time.Now().UTC()); err != nil || !ok {
		t.Fatalf("complete persisted outcome after restart: ok=%v err=%v", ok, err)
	}
	caseAfter, err := restarted.GetRepairCase(ctx, c.ID)
	if err != nil || caseAfter.State != coderepair.StatePatchReady || caseAfter.Version != 4 {
		t.Fatalf("verified case state=%+v err=%v", caseAfter, err)
	}
	attempt, err := restarted.GetRepairAttempt(ctx, job.AttemptID)
	if err != nil || attempt.Status != "succeeded" {
		t.Fatalf("completed attempt=%+v err=%v", attempt, err)
	}
}

func TestRepairInvestigationOutcomeFailsClosedAndRollsBackIntegration(t *testing.T) {
	store := isolatedRepairStore(t)
	ctx := context.Background()
	c, job := claimedOutcomeFixture(t, store)
	report := []byte(`{"status":"failed","recipe_id":"go-test-workload","before_exit":1,"after_exit":1}`)
	outcome := coderepair.InvestigationOutcome{JobID: job.ID, LeaseToken: job.LeaseToken, CaseID: c.ID,
		AttemptID: job.AttemptID, ExpectedVersion: 3, State: coderepair.StateFailed,
		ErrorCode: "TESTS_FAILED", ErrorMessage: "regression still fails", ReportJSON: report, RecordedAt: time.Now().UTC()}
	withPatch := outcome
	withPatch.Patch = []byte("fallback patch")
	if _, err := store.RecordRepairInvestigationOutcome(ctx, withPatch); err == nil {
		t.Fatal("failure outcome accepted a fallback patch")
	}
	if _, err := store.db.ExecContext(ctx, `CREATE FUNCTION fail_repair_report() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.kind = 'investigation_report' THEN RAISE EXCEPTION 'injected artifact failure'; END IF;
		RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER fail_repair_report_insert BEFORE INSERT ON repair_artifacts
		FOR EACH ROW EXECUTE FUNCTION fail_repair_report()`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordRepairInvestigationOutcome(ctx, outcome); err == nil {
		t.Fatal("injected artifact failure did not roll back outcome")
	}
	caseAfter, err := store.GetRepairCase(ctx, c.ID)
	if err != nil || caseAfter.State != coderepair.StateInvestigating || caseAfter.Version != 3 {
		t.Fatalf("artifact failure left partial case state: %+v %v", caseAfter, err)
	}
	attempt, err := store.GetRepairAttempt(ctx, job.AttemptID)
	if err != nil || attempt.ErrorCode != "" {
		t.Fatalf("artifact failure left partial attempt: %+v %v", attempt, err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER fail_repair_report_insert ON repair_artifacts`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP FUNCTION fail_repair_report()`); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.RecordRepairInvestigationOutcome(ctx, outcome); err != nil || !ok {
		t.Fatalf("record explicit test failure: ok=%v err=%v", ok, err)
	}
	if ok, err := store.CompleteRepairJob(ctx, job.ID, job.LeaseToken, time.Now().UTC()); err != nil || !ok {
		t.Fatalf("complete explicit failure: ok=%v err=%v", ok, err)
	}
	attempt, err = store.GetRepairAttempt(ctx, job.AttemptID)
	if err != nil || attempt.Status != "failed" || attempt.ErrorCode != "TESTS_FAILED" ||
		!strings.Contains(attempt.ErrorMessage, "regression still fails") {
		t.Fatalf("terminal attempt lost failure reason: %+v %v", attempt, err)
	}
}

func TestRepairInvestigationFailureSurvivesCrashBeforeJobCompletionIntegration(t *testing.T) {
	store := isolatedRepairStore(t)
	ctx := context.Background()
	c, job := claimedOutcomeFixture(t, store)
	started := time.Now().UTC()
	report := []byte(`{"status":"blocked","recipe_id":"go-test-workload","before_exit":1,"after_exit":0}`)
	outcome := coderepair.InvestigationOutcome{JobID: job.ID, LeaseToken: job.LeaseToken, CaseID: c.ID,
		AttemptID: job.AttemptID, ExpectedVersion: 3, State: coderepair.StateBlocked,
		ErrorCode: "PROVIDER_ERROR", ErrorMessage: "coding provider unavailable", ReportJSON: report, RecordedAt: started}
	if ok, err := store.RecordRepairInvestigationOutcome(ctx, outcome); err != nil || !ok {
		t.Fatalf("record blocked outcome: ok=%v err=%v", ok, err)
	}
	restarted := reopenRepairStore(t, store)
	count, err := restarted.RecoverRepairJobs(ctx, job.LeaseUntil.Add(time.Second))
	if err != nil || count != 1 {
		t.Fatalf("reconcile after crash: count=%d err=%v", count, err)
	}
	finalJob, err := restarted.GetRepairJob(ctx, job.ID)
	if err != nil || finalJob.Status != coderepair.JobSucceeded {
		t.Fatalf("reconciled job=%+v err=%v", finalJob, err)
	}
	attempt, err := restarted.GetRepairAttempt(ctx, job.AttemptID)
	if err != nil || attempt.Status != "blocked" || attempt.ErrorCode != "PROVIDER_ERROR" {
		t.Fatalf("reconciled attempt=%+v err=%v", attempt, err)
	}
	if ok, err := store.CompleteRepairJob(ctx, job.ID, job.LeaseToken, job.LeaseUntil.Add(2*time.Second)); err != nil || ok {
		t.Fatalf("stale worker completed reconciled job: ok=%v err=%v", ok, err)
	}
}
