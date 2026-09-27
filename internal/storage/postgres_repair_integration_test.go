package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	repairrunner "github.com/Cyaside/Triovexa/internal/coderepair/runner"
	"github.com/Cyaside/Triovexa/internal/domain"
)

func repairTestEvent(caseID, name string, now time.Time) coderepair.Event {
	return coderepair.Event{ID: uuid.NewString(), CaseID: caseID, ActorID: "operator-test",
		Type: name, DetailsJSON: `{}`, CreatedAt: now}
}

func isolatedRepairStore(t *testing.T) *PostgresStore {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	admin, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := "repair_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) })
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	store, err := NewPostgresStore(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func makeInvestigationTestCase(t *testing.T, store *PostgresStore, maxAttempts int) (coderepair.Case, coderepair.Approval, coderepair.Attempt, coderepair.Job) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	incidentID := uuid.NewString()
	incident := domain.Incident{ID: incidentID, ExternalAlertID: incidentID, AlertSource: "repair-test",
		Title: "worker fails after deployment", ServiceName: "queue-worker-" + incidentID[:8], Environment: "staging",
		Severity: "high", State: domain.IncidentStateEscalated, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateIncident(ctx, incident); err != nil {
		t.Fatal(err)
	}
	binding := coderepair.RepositoryBinding{ID: uuid.NewString(), ServiceName: incident.ServiceName,
		Environment: incident.Environment, RepositoryURL: "https://github.com/Cyaside/Triovexa",
		BaseRef: "repair-code-workflow", AllowedPaths: []string{"internal/workload"},
		TestRecipes: []string{"go-test-workload"}, PolicyVersion: "repair-v1", Enabled: true,
		CreatedAt: now, UpdatedAt: now}
	if err := store.CreateRepositoryBinding(ctx, binding); err != nil {
		t.Fatal(err)
	}
	c := coderepair.Case{ID: uuid.NewString(), IncidentID: incidentID, BindingID: binding.ID,
		BaseSHA: uuid.NewString(), DeployedSHA: uuid.NewString(), PolicyVersion: binding.PolicyVersion,
		State: coderepair.StateProposed, Version: 1, CreatedBy: "operator-test", CreatedAt: now, UpdatedAt: now}
	var err error
	c.ScopeDigest, err = coderepair.ScopeDigest(c, binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRepairCase(ctx, c, repairTestEvent(c.ID, "case_proposed", now)); err != nil {
		t.Fatal(err)
	}
	changed, err := store.TransitionRepairCase(ctx, c.ID, coderepair.StateProposed, 1,
		coderepair.StateAwaitingInvestigationApproval, repairTestEvent(c.ID, "investigation_requested", now))
	if err != nil || !changed {
		t.Fatalf("request investigation: changed=%v err=%v", changed, err)
	}
	c.State = coderepair.StateAwaitingInvestigationApproval
	c.Version = 2
	attempt := coderepair.Attempt{ID: uuid.NewString(), CaseID: c.ID, Number: 1,
		Status: coderepair.JobQueued, CreatedAt: now}
	payload, _ := json.Marshal(map[string]any{"case_id": c.ID, "attempt_id": attempt.ID, "expected_version": int64(3)})
	job := coderepair.Job{ID: uuid.NewString(), CaseID: c.ID, AttemptID: attempt.ID,
		Type: coderepair.JobTypeInvestigation, DedupKey: coderepair.InvestigationDedupKey(c.ID, 1),
		PayloadJSON: string(payload), Status: coderepair.JobQueued, MaxAttempts: maxAttempts,
		AvailableAt: now, CreatedAt: now}
	approval := coderepair.Approval{ID: uuid.NewString(), CaseID: c.ID, CaseVersion: 2,
		Phase: "investigation", ActorID: "operator-test", Decision: "approved",
		ScopeDigest: c.ScopeDigest, PolicyVersion: c.PolicyVersion,
		CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
	return c, approval, attempt, job
}

func TestRepairApprovalAndLeaseFencingIntegration(t *testing.T) {
	store := isolatedRepairStore(t)
	ctx := context.Background()
	c, approval, attempt, job := makeInvestigationTestCase(t, store, 2)

	bad := approval
	bad.ID = uuid.NewString()
	bad.ScopeDigest = "modified"
	if ok, err := store.ApproveRepairInvestigation(ctx, bad, attempt, job,
		repairTestEvent(c.ID, "investigation_approved", time.Now().UTC())); err == nil || ok {
		t.Fatalf("modified approval was accepted: ok=%v err=%v", ok, err)
	}

	var wg sync.WaitGroup
	results := make(chan bool, 2)
	errorsCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := approval
			a.ID = uuid.NewString()
			at := attempt
			at.ID = uuid.NewString()
			j := job
			j.ID = uuid.NewString()
			j.AttemptID = at.ID
			payload, _ := json.Marshal(map[string]any{"case_id": c.ID, "attempt_id": at.ID, "expected_version": int64(3)})
			j.PayloadJSON = string(payload)
			ok, err := store.ApproveRepairInvestigation(ctx, a, at, j,
				repairTestEvent(c.ID, "investigation_approved", time.Now().UTC()))
			results <- ok
			errorsCh <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errorsCh)
	won := 0
	for ok := range results {
		if ok {
			won++
		}
	}
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if won != 1 {
		t.Fatalf("concurrent approval winners=%d, want 1", won)
	}
	current, err := store.GetRepairCase(ctx, c.ID)
	if err != nil || current.State != coderepair.StateInvestigating || current.Version != 3 {
		t.Fatalf("case after approval=%+v err=%v", current, err)
	}
	if changed, err := store.TransitionRepairCase(ctx, c.ID, coderepair.StateAwaitingInvestigationApproval, 2,
		coderepair.StateInvestigating, repairTestEvent(c.ID, "stale_transition", time.Now().UTC())); err != nil || changed {
		t.Fatalf("stale state transition changed case: changed=%v err=%v", changed, err)
	}
	for table := range map[string]int{"repair_approvals": 1, "repair_attempts": 1, "repair_jobs": 1, "repair_events": 3} {
		var count int
		if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE case_id=$1`, c.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		want := map[string]int{"repair_approvals": 1, "repair_attempts": 1, "repair_jobs": 1, "repair_events": 3}[table]
		if count != want {
			t.Fatalf("%s count=%d, want %d", table, count, want)
		}
	}

	now := time.Now().UTC()
	claimed, err := store.ClaimRepairJob(ctx, "worker-one", now, 500*time.Millisecond)
	if err != nil || claimed.CaseID != c.ID || claimed.Attempts != 1 {
		t.Fatalf("first claim=%+v err=%v", claimed, err)
	}
	if ok, err := store.RenewRepairJobLease(ctx, claimed.ID, "wrong-token", now.Add(100*time.Millisecond), time.Second); err != nil || ok {
		t.Fatalf("wrong token renewed lease: ok=%v err=%v", ok, err)
	}
	if ok, err := store.RenewRepairJobLease(ctx, claimed.ID, claimed.LeaseToken, now.Add(100*time.Millisecond), time.Second); err != nil || !ok {
		t.Fatalf("current token failed renewal: ok=%v err=%v", ok, err)
	}
	if _, err := store.ClaimRepairJob(ctx, "worker-two", now.Add(600*time.Millisecond), time.Second); !errors.Is(err, coderepair.ErrNoJobAvailable) {
		t.Fatalf("unexpired lease was reclaimed: %v", err)
	}
	reclaimed, err := store.ClaimRepairJob(ctx, "worker-two", now.Add(1200*time.Millisecond), time.Second)
	if err != nil || reclaimed.ID != claimed.ID || reclaimed.LeaseToken == claimed.LeaseToken || reclaimed.Attempts != 2 {
		t.Fatalf("reclaim=%+v err=%v", reclaimed, err)
	}
	if ok, err := store.CompleteRepairJob(ctx, claimed.ID, claimed.LeaseToken, now.Add(1300*time.Millisecond)); err != nil || ok {
		t.Fatalf("stale worker completed job: ok=%v err=%v", ok, err)
	}
	if ok, err := store.FailRepairJob(ctx, reclaimed.ID, reclaimed.LeaseToken, "provider token=secret-value", now.Add(1300*time.Millisecond), now.Add(2*time.Second), true); err != nil || !ok {
		t.Fatalf("terminal failure: ok=%v err=%v", ok, err)
	}
	blocked, err := store.GetRepairCase(ctx, c.ID)
	if err != nil || blocked.State != coderepair.StateBlocked {
		t.Fatalf("case after terminal failure=%+v err=%v", blocked, err)
	}
	finalJob, err := store.GetRepairJob(ctx, reclaimed.ID)
	if err != nil || finalJob.Status != coderepair.JobDeadLetter || finalJob.LastError != "provider token=[REDACTED]" {
		t.Fatalf("job after failure=%+v err=%v", finalJob, err)
	}
}

func TestRepairStartupRecoveryAndArtifactAuditIntegration(t *testing.T) {
	store := isolatedRepairStore(t)
	ctx := context.Background()
	c, approval, attempt, job := makeInvestigationTestCase(t, store, 1)
	ok, err := store.ApproveRepairInvestigation(ctx, approval, attempt, job,
		repairTestEvent(c.ID, "investigation_approved", time.Now().UTC()))
	if err != nil || !ok {
		t.Fatalf("approve investigation: ok=%v err=%v", ok, err)
	}
	digest := sha256.Sum256([]byte("fixture diff"))
	artifact := coderepair.Artifact{ID: uuid.NewString(), AttemptID: attempt.ID, Kind: "diff",
		ContentSHA256: hex.EncodeToString(digest[:]), ArtifactRef: "artifacts/code-repair/diff.patch",
		ByteSize: 12, CreatedAt: time.Now().UTC()}
	if err := store.AddRepairArtifact(ctx, artifact,
		repairTestEvent(c.ID, "artifact_recorded", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	claimed, err := store.ClaimRepairJob(ctx, "worker-one", now, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	count, err := store.RecoverRepairJobs(ctx, now.Add(300*time.Millisecond))
	if err != nil || count != 1 {
		t.Fatalf("startup recovery count=%d err=%v", count, err)
	}
	count, err = store.RecoverRepairJobs(ctx, now.Add(400*time.Millisecond))
	if err != nil || count != 0 {
		t.Fatalf("repeated recovery count=%d err=%v", count, err)
	}
	if ok, err := store.CompleteRepairJob(ctx, claimed.ID, claimed.LeaseToken, now.Add(500*time.Millisecond)); err != nil || ok {
		t.Fatalf("recovered job accepted stale completion: ok=%v err=%v", ok, err)
	}
	recoveredCase, err := store.GetRepairCase(ctx, c.ID)
	if err != nil || recoveredCase.State != coderepair.StateBlocked {
		t.Fatalf("case after startup recovery=%+v err=%v", recoveredCase, err)
	}
	events, err := store.ListRepairEvents(ctx, c.ID)
	if err != nil || len(events) != 5 || events[3].Type != "artifact_recorded" || events[4].Type != "investigation_blocked" {
		t.Fatalf("repair audit after recovery=%+v err=%v", events, err)
	}
}

func TestRepairRunnerRenewsLeaseUntilHandlerCompletesIntegration(t *testing.T) {
	store := isolatedRepairStore(t)
	ctx := context.Background()
	c, approval, attempt, job := makeInvestigationTestCase(t, store, 2)
	ok, err := store.ApproveRepairInvestigation(ctx, approval, attempt, job,
		repairTestEvent(c.ID, "investigation_approved", time.Now().UTC()))
	if err != nil || !ok {
		t.Fatalf("approve investigation: ok=%v err=%v", ok, err)
	}
	handled := make(chan struct{}, 1)
	r, err := repairrunner.New(store, func(ctx context.Context, claimed coderepair.Job) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(650 * time.Millisecond):
		}
		changed, err := store.TransitionRepairCase(ctx, c.ID, coderepair.StateInvestigating, 3,
			coderepair.StatePatchReady, repairTestEvent(c.ID, "patch_ready", time.Now().UTC()))
		if err != nil || !changed {
			return errors.New("could not record patch-ready result")
		}
		handled <- struct{}{}
		return nil
	}, nil, repairrunner.Config{Workers: 1, Lease: 300 * time.Millisecond,
		MaxRun: 3 * time.Second, PollInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- r.Run(runCtx) }()
	defer func() { cancel(); <-done }()
	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("repair handler did not complete across multiple lease periods")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		result, err := store.GetRepairJob(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if result.Status == coderepair.JobSucceeded {
			if result.Attempts != 1 {
				t.Fatalf("renewed job was reclaimed: attempts=%d", result.Attempts)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("repair runner did not persist successful completion")
}

func TestRepairRecoveryAfterOutcomeBeforeJobCompletionIntegration(t *testing.T) {
	store := isolatedRepairStore(t)
	ctx := context.Background()
	c, approval, attempt, job := makeInvestigationTestCase(t, store, 2)
	ok, err := store.ApproveRepairInvestigation(ctx, approval, attempt, job,
		repairTestEvent(c.ID, "investigation_approved", time.Now().UTC()))
	if err != nil || !ok {
		t.Fatalf("approve investigation: ok=%v err=%v", ok, err)
	}
	now := time.Now().UTC()
	claimed, err := store.ClaimRepairJob(ctx, "worker-before-crash", now, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := store.TransitionRepairCase(ctx, c.ID, coderepair.StateInvestigating, 3,
		coderepair.StatePatchReady, repairTestEvent(c.ID, "patch_ready", now.Add(100*time.Millisecond)))
	if err != nil || !changed {
		t.Fatalf("persist outcome before crash: changed=%v err=%v", changed, err)
	}
	count, err := store.RecoverRepairJobs(ctx, now.Add(300*time.Millisecond))
	if err != nil || count != 1 {
		t.Fatalf("reconcile finished case: count=%d err=%v", count, err)
	}
	finalJob, err := store.GetRepairJob(ctx, claimed.ID)
	if err != nil || finalJob.Status != coderepair.JobSucceeded {
		t.Fatalf("reconciled job=%+v err=%v", finalJob, err)
	}
	if ok, err := store.CompleteRepairJob(ctx, claimed.ID, claimed.LeaseToken, now.Add(400*time.Millisecond)); err != nil || ok {
		t.Fatalf("stale completion changed reconciled job: ok=%v err=%v", ok, err)
	}
	var attemptStatus string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM repair_attempts WHERE id=$1`, attempt.ID).Scan(&attemptStatus); err != nil || attemptStatus != "succeeded" {
		t.Fatalf("reconciled attempt status=%q err=%v", attemptStatus, err)
	}
}
