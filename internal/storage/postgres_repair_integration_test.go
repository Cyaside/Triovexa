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

func reopenRepairStore(t *testing.T, previous *PostgresStore) *PostgresStore {
	t.Helper()
	var schema string
	if err := previous.db.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
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
	deployedSHA := strings.Repeat("b", 40)
	evidenceMetadata, _ := json.Marshal(map[string]any{"target": incident.ServiceName, "complete": true, "deployed_revision": deployedSHA})
	evidenceItems := []domain.EvidenceItem{{
		ID: uuid.NewString(), IncidentID: incidentID, Type: "metric", Source: "workload-control",
		Snippet: "worker unhealthy", Timestamp: now, MetadataJSON: string(evidenceMetadata),
	}, {
		ID: uuid.NewString(), IncidentID: incidentID, Type: "log", Source: "grafana-loki",
		Snippet: "worker failed to process job", Timestamp: now, MetadataJSON: `{}`,
	}}
	if err := store.SaveEvidenceItems(ctx, evidenceItems); err != nil {
		t.Fatal(err)
	}
	c := coderepair.Case{ID: uuid.NewString(), IncidentID: incidentID, BindingID: binding.ID,
		BaseSHA: strings.Repeat("a", 40), DeployedSHA: deployedSHA, PolicyVersion: binding.PolicyVersion,
		State: coderepair.StateProposed, Version: 1, CreatedBy: "operator-test", CreatedAt: now, UpdatedAt: now}
	var err error
	c.ScopeDigest, err = coderepair.ScopeDigest(c, binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRepairCase(ctx, c, repairTestEvent(c.ID, "case_proposed", now)); err != nil {
		t.Fatal(err)
	}
	snapshot, err := coderepair.BuildEvidenceSnapshot(incident, evidenceItems, now,
		coderepair.EvidenceLimits{MaxItems: 10, MaxSnippetBytes: 2048, MaxTotalBytes: 4096, MaxAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO repair_evidence
		(id,case_id,source,observed_at,complete,content_sha256,artifact_ref,created_at,snapshot_json)
		VALUES ($1,$2,'incident_snapshot',$3,true,$4,'database',$3,$5::jsonb)`,
		uuid.NewString(), c.ID, now, snapshot.SHA256, string(snapshotJSON)); err != nil {
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

func TestRepairCaseRejectsRevisionNotObservedByWorkloadIntegration(t *testing.T) {
	store := isolatedRepairStore(t)
	ctx := context.Background()
	c, _, _, _ := makeInvestigationTestCase(t, store, 1)
	binding, err := store.GetActiveRepositoryBinding(ctx, "queue-worker-"+c.IncidentID[:8], "staging")
	if err != nil {
		t.Fatal(err)
	}
	bad := c
	bad.ID = uuid.NewString()
	bad.State = coderepair.StateProposed
	bad.Version = 1
	bad.DeployedSHA = strings.Repeat("c", 40)
	bad.ScopeDigest, err = coderepair.ScopeDigest(bad, binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRepairCase(ctx, bad, repairTestEvent(bad.ID, "case_proposed", time.Now().UTC())); err == nil ||
		!strings.Contains(err.Error(), "deployed revision") {
		t.Fatalf("case accepted unobserved deployed revision: %v", err)
	}
}

func TestRepairProposalStoresEvidenceWithCaseAndAuditIntegration(t *testing.T) {
	store := isolatedRepairStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	incidentID := uuid.NewString()
	incident := domain.Incident{ID: incidentID, ExternalAlertID: incidentID, AlertSource: "repair-test",
		Title: "worker processing failure", ServiceName: "queue-worker-" + incidentID[:8], Environment: "staging",
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
	deployedSHA := strings.Repeat("b", 40)
	metadata, _ := json.Marshal(map[string]any{"target": incident.ServiceName, "complete": true, "deployed_revision": deployedSHA})
	items := []domain.EvidenceItem{
		{ID: uuid.NewString(), IncidentID: incident.ID, Type: "metric", Source: "workload-control",
			Snippet: "backlog rising", Timestamp: now, MetadataJSON: string(metadata)},
		{ID: uuid.NewString(), IncidentID: incident.ID, Type: "log", Source: "grafana-loki",
			Snippet: "decode failed", Timestamp: now, MetadataJSON: `{}`},
	}
	if err := store.SaveEvidenceItems(ctx, items); err != nil {
		t.Fatal(err)
	}
	limits := coderepair.EvidenceLimits{MaxItems: 10, MaxSnippetBytes: 2048, MaxTotalBytes: 4096, MaxAge: time.Minute}
	service, err := coderepair.NewProposalService(store, func(context.Context, coderepair.RepositoryBinding) (string, error) {
		return strings.Repeat("a", 40), nil
	}, limits)
	if err != nil {
		t.Fatal(err)
	}
	c, expected, err := service.Propose(ctx, incident.ID, "operator-test", now)
	if err != nil {
		t.Fatal(err)
	}
	if c.State != coderepair.StateAwaitingInvestigationApproval {
		t.Fatalf("proposal state = %q", c.State)
	}
	stored, err := store.GetRepairEvidenceSnapshot(ctx, c.ID)
	if err != nil || !stored.VerifyDigest() || stored.SHA256 != expected.SHA256 {
		t.Fatalf("stored evidence does not match proposal: %+v, %v", stored, err)
	}
	events, err := store.ListRepairEvents(ctx, c.ID)
	if err != nil || len(events) != 1 || events[0].Type != "investigation_requested" {
		t.Fatalf("proposal audit missing: %+v, %v", events, err)
	}
	authorizer, err := coderepair.NewInvestigationAuthorizationService(store)
	if err != nil {
		t.Fatal(err)
	}
	_, attempt, job, err := authorizer.Authorize(ctx, c.ID, "operator-test",
		coderepair.AgentSelection{Provider: "openai-compatible", Model: "glm-5.3-flash", PromptVersion: "repair-v1"},
		time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Status != coderepair.JobQueued || job.AttemptID != attempt.ID {
		t.Fatalf("approval did not create a queued investigation: %+v %+v", attempt, job)
	}
	storedJob, err := store.GetRepairJob(ctx, job.ID)
	if err != nil || storedJob.ID != job.ID {
		t.Fatalf("investigation job was not committed: %+v, %v", storedJob, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE repair_evidence SET snapshot_json=jsonb_set(snapshot_json,
		'{deployed_revision}', '"tampered"') WHERE case_id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetRepairEvidenceSnapshot(ctx, c.ID); err == nil {
		t.Fatal("tampered evidence snapshot was accepted")
	}
}

func TestRepairApprovalRejectsStaleDeployedRevisionIntegration(t *testing.T) {
	store := isolatedRepairStore(t)
	ctx := context.Background()
	c, approval, attempt, job := makeInvestigationTestCase(t, store, 1)
	if _, err := store.db.ExecContext(ctx, `UPDATE evidence_items SET timestamp=$1 WHERE incident_id=$2`,
		time.Now().UTC().Add(-2*time.Minute), c.IncidentID); err != nil {
		t.Fatal(err)
	}
	ok, err := store.ApproveRepairInvestigation(ctx, approval, attempt, job,
		repairTestEvent(c.ID, "investigation_approved", time.Now().UTC()))
	if ok || err == nil || !strings.Contains(err.Error(), "fresh matching deployed revision") {
		t.Fatalf("stale deployed revision approved: ok=%v err=%v", ok, err)
	}
	current, err := store.GetRepairCase(ctx, c.ID)
	if err != nil || current.State != coderepair.StateAwaitingInvestigationApproval {
		t.Fatalf("approval mutated case after stale evidence: case=%+v err=%v", current, err)
	}
}

func TestRepairApprovalRejectsStaleLogIntegration(t *testing.T) {
	store := isolatedRepairStore(t)
	ctx := context.Background()
	c, approval, attempt, job := makeInvestigationTestCase(t, store, 1)
	if _, err := store.db.ExecContext(ctx, `UPDATE evidence_items SET timestamp=$1
		WHERE incident_id=$2 AND type='log'`, time.Now().UTC().Add(-2*time.Minute), c.IncidentID); err != nil {
		t.Fatal(err)
	}
	ok, err := store.ApproveRepairInvestigation(ctx, approval, attempt, job,
		repairTestEvent(c.ID, "investigation_approved", time.Now().UTC()))
	if ok || err == nil || !strings.Contains(err.Error(), "fresh log evidence") {
		t.Fatalf("stale log approved: ok=%v err=%v", ok, err)
	}
}

func TestRepairApprovalRejectsMissingSnapshotIntegration(t *testing.T) {
	store := isolatedRepairStore(t)
	ctx := context.Background()
	c, approval, attempt, job := makeInvestigationTestCase(t, store, 1)
	if _, err := store.db.ExecContext(ctx, `DELETE FROM repair_evidence WHERE case_id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	ok, err := store.ApproveRepairInvestigation(ctx, approval, attempt, job,
		repairTestEvent(c.ID, "investigation_approved", time.Now().UTC()))
	if ok || err == nil || !strings.Contains(err.Error(), "captured evidence snapshot") {
		t.Fatalf("case without snapshot approved: ok=%v err=%v", ok, err)
	}
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
	var attemptStatus string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM repair_attempts WHERE id=$1`, claimed.AttemptID).Scan(&attemptStatus); err != nil || attemptStatus != "running" {
		t.Fatalf("claimed attempt status=%q err=%v", attemptStatus, err)
	}
	if ok, err := store.CompleteRepairJob(ctx, claimed.ID, claimed.LeaseToken, now.Add(50*time.Millisecond)); err == nil || ok {
		t.Fatalf("job completed without a persisted case outcome: ok=%v err=%v", ok, err)
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

func TestRepairTransientFailureRequeuesAttemptIntegration(t *testing.T) {
	store := isolatedRepairStore(t)
	ctx := context.Background()
	c, approval, attempt, job := makeInvestigationTestCase(t, store, 2)
	if ok, err := store.ApproveRepairInvestigation(ctx, approval, attempt, job,
		repairTestEvent(c.ID, "investigation_approved", time.Now().UTC())); err != nil || !ok {
		t.Fatalf("approve investigation: ok=%v err=%v", ok, err)
	}
	now := time.Now().UTC()
	first, err := store.ClaimRepairJob(ctx, "worker-one", now, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := store.FailRepairJob(ctx, first.ID, first.LeaseToken, "transient provider outage",
		now.Add(time.Second), now.Add(2*time.Second), false); err != nil || !ok {
		t.Fatalf("requeue transient failure: ok=%v err=%v", ok, err)
	}
	var status string
	var startedAt time.Time
	if err := store.db.QueryRowContext(ctx, `SELECT status,started_at FROM repair_attempts WHERE id=$1`, attempt.ID).
		Scan(&status, &startedAt); err != nil || status != "queued" || startedAt.IsZero() {
		t.Fatalf("requeued attempt status=%q started=%v err=%v", status, startedAt, err)
	}
	second, err := store.ClaimRepairJob(ctx, "worker-two", now.Add(2*time.Second), 10*time.Second)
	if err != nil || second.Attempts != 2 || second.LeaseToken == first.LeaseToken {
		t.Fatalf("retry claim=%+v err=%v", second, err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM repair_attempts WHERE id=$1`, attempt.ID).
		Scan(&status); err != nil || status != "running" {
		t.Fatalf("retried attempt status=%q err=%v", status, err)
	}
}

func TestRepairCaseSurvivesRestartAtDurabilityBoundariesIntegration(t *testing.T) {
	firstStore := isolatedRepairStore(t)
	ctx := context.Background()
	c, approval, attempt, job := makeInvestigationTestCase(t, firstStore, 2)
	secondStore := reopenRepairStore(t, firstStore)
	if got, err := secondStore.GetRepairCase(ctx, c.ID); err != nil || got.State != coderepair.StateAwaitingInvestigationApproval {
		t.Fatalf("case lost before approval: %+v, %v", got, err)
	}
	if _, err := secondStore.GetRepairEvidenceSnapshot(ctx, c.ID); err != nil {
		t.Fatalf("evidence lost before approval: %v", err)
	}
	if ok, err := secondStore.ApproveRepairInvestigation(ctx, approval, attempt, job,
		repairTestEvent(c.ID, "investigation_approved", time.Now().UTC())); err != nil || !ok {
		t.Fatalf("approval after restart: ok=%v err=%v", ok, err)
	}
	thirdStore := reopenRepairStore(t, secondStore)
	if got, err := thirdStore.GetRepairJob(ctx, job.ID); err != nil || got.Status != coderepair.JobQueued {
		t.Fatalf("job lost after approval: %+v, %v", got, err)
	}
	now := time.Now().UTC()
	firstClaim, err := thirdStore.ClaimRepairJob(ctx, "worker-before-crash", now, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("pre-crash patch"))
	artifact := coderepair.Artifact{ID: uuid.NewString(), AttemptID: attempt.ID, Kind: "trace",
		ContentSHA256: hex.EncodeToString(digest[:]), ArtifactRef: "artifacts/code-repair/trace.json",
		ByteSize: 15, CreatedAt: now}
	if err := thirdStore.AddRepairArtifact(ctx, artifact,
		repairTestEvent(c.ID, "artifact_recorded", now)); err != nil {
		t.Fatal(err)
	}
	fourthStore := reopenRepairStore(t, thirdStore)
	var artifactCount int
	if err := fourthStore.db.QueryRowContext(ctx, `SELECT count(*) FROM repair_artifacts WHERE attempt_id=$1`, attempt.ID).
		Scan(&artifactCount); err != nil || artifactCount != 1 {
		t.Fatalf("artifact manifest lost after restart: count=%d err=%v", artifactCount, err)
	}
	if count, err := fourthStore.RecoverRepairJobs(ctx, now.Add(2*time.Second)); err != nil || count != 0 {
		t.Fatalf("recovery stole unexpired lease: count=%d err=%v", count, err)
	}
	if _, err := fourthStore.ClaimRepairJob(ctx, "worker-after-crash", now.Add(2*time.Second), time.Second); !errors.Is(err, coderepair.ErrNoJobAvailable) {
		t.Fatalf("unexpired job was claimed: %v", err)
	}
	if count, err := fourthStore.RecoverRepairJobs(ctx, now.Add(4*time.Second)); err != nil || count != 0 {
		t.Fatalf("recoverable lease was prematurely dead-lettered: count=%d err=%v", count, err)
	}
	secondClaim, err := fourthStore.ClaimRepairJob(ctx, "worker-after-crash", now.Add(4*time.Second), 3*time.Second)
	if err != nil || secondClaim.ID != firstClaim.ID || secondClaim.LeaseToken == firstClaim.LeaseToken || secondClaim.Attempts != 2 {
		t.Fatalf("reclaimed job=%+v err=%v", secondClaim, err)
	}
	if ok, err := fourthStore.FailRepairJob(ctx, firstClaim.ID, firstClaim.LeaseToken, "stale worker",
		now.Add(5*time.Second), now.Add(6*time.Second), true); err != nil || ok {
		t.Fatalf("stale worker changed job: ok=%v err=%v", ok, err)
	}
	fifthStore := reopenRepairStore(t, fourthStore)
	if count, err := fifthStore.RecoverRepairJobs(ctx, now.Add(8*time.Second)); err != nil || count != 1 {
		t.Fatalf("final crashed lease was not reconciled: count=%d err=%v", count, err)
	}
	if count, err := fifthStore.RecoverRepairJobs(ctx, now.Add(9*time.Second)); err != nil || count != 0 {
		t.Fatalf("repeated recovery changed state: count=%d err=%v", count, err)
	}
	finalCase, caseErr := fifthStore.GetRepairCase(ctx, c.ID)
	finalJob, jobErr := fifthStore.GetRepairJob(ctx, job.ID)
	if caseErr != nil || jobErr != nil || finalCase.State != coderepair.StateBlocked || finalJob.Status != coderepair.JobDeadLetter {
		t.Fatalf("durable final state: case=%+v (%v), job=%+v (%v)", finalCase, caseErr, finalJob, jobErr)
	}
	for table := range map[string]bool{"repair_approvals": true, "repair_attempts": true, "repair_jobs": true} {
		var count int
		if err := fifthStore.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE case_id=$1`, c.ID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s rows=%d err=%v; restart duplicated work", table, count, err)
		}
	}
}

func TestRepairApprovalRollsBackEveryRecordWhenJobInsertFailsIntegration(t *testing.T) {
	store := isolatedRepairStore(t)
	ctx := context.Background()
	firstCase, firstApproval, firstAttempt, firstJob := makeInvestigationTestCase(t, store, 1)
	if ok, err := store.ApproveRepairInvestigation(ctx, firstApproval, firstAttempt, firstJob,
		repairTestEvent(firstCase.ID, "investigation_approved", time.Now().UTC())); err != nil || !ok {
		t.Fatalf("seed approval: ok=%v err=%v", ok, err)
	}
	secondCase, secondApproval, secondAttempt, secondJob := makeInvestigationTestCase(t, store, 1)
	secondJob.ID = firstJob.ID // failure occurs after approval and attempt inserts
	if ok, err := store.ApproveRepairInvestigation(ctx, secondApproval, secondAttempt, secondJob,
		repairTestEvent(secondCase.ID, "investigation_approved", time.Now().UTC())); err == nil || ok {
		t.Fatalf("duplicate job ID did not fail atomically: ok=%v err=%v", ok, err)
	}
	current, err := store.GetRepairCase(ctx, secondCase.ID)
	if err != nil || current.State != coderepair.StateAwaitingInvestigationApproval || current.Version != 2 {
		t.Fatalf("failed transaction moved case: %+v, %v", current, err)
	}
	for table := range map[string]bool{"repair_approvals": true, "repair_attempts": true, "repair_jobs": true} {
		var count int
		if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE case_id=$1`, secondCase.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("failed transaction left %s rows=%d err=%v", table, count, err)
		}
	}
	events, err := store.ListRepairEvents(ctx, secondCase.ID)
	if err != nil || len(events) != 2 {
		t.Fatalf("failed transaction left approval audit: %+v, %v", events, err)
	}
	secondJob.ID = uuid.NewString()
	if ok, err := store.ApproveRepairInvestigation(ctx, secondApproval, secondAttempt, secondJob,
		repairTestEvent(secondCase.ID, "investigation_approved", time.Now().UTC())); err != nil || !ok {
		t.Fatalf("valid retry after rollback: ok=%v err=%v", ok, err)
	}
}

func TestRepairArtifactAndAuditRollbackTogetherIntegration(t *testing.T) {
	store := isolatedRepairStore(t)
	ctx := context.Background()
	c, approval, attempt, job := makeInvestigationTestCase(t, store, 1)
	if ok, err := store.ApproveRepairInvestigation(ctx, approval, attempt, job,
		repairTestEvent(c.ID, "investigation_approved", time.Now().UTC())); err != nil || !ok {
		t.Fatalf("approve investigation: ok=%v err=%v", ok, err)
	}
	events, err := store.ListRepairEvents(ctx, c.ID)
	if err != nil || len(events) == 0 {
		t.Fatalf("load seed audit events: %+v, %v", events, err)
	}
	digest := sha256.Sum256([]byte("bounded patch"))
	artifact := coderepair.Artifact{ID: uuid.NewString(), AttemptID: attempt.ID, Kind: "diff",
		ContentSHA256: hex.EncodeToString(digest[:]), ArtifactRef: "artifacts/code-repair/diff.patch",
		ByteSize: 13, CreatedAt: time.Now().UTC()}
	duplicateAudit := repairTestEvent(c.ID, "artifact_recorded", time.Now().UTC())
	duplicateAudit.ID = events[0].ID
	if err := store.AddRepairArtifact(ctx, artifact, duplicateAudit); err == nil {
		t.Fatal("duplicate audit ID did not roll back artifact insert")
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM repair_artifacts WHERE attempt_id=$1`, attempt.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed artifact transaction left %d manifests: %v", count, err)
	}
	if err := store.AddRepairArtifact(ctx, artifact, repairTestEvent(c.ID, "artifact_recorded", time.Now().UTC())); err != nil {
		t.Fatalf("artifact insert after rollback: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM repair_artifacts WHERE attempt_id=$1`, attempt.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("committed artifact manifests=%d: %v", count, err)
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
		case <-time.After(2500 * time.Millisecond):
		}
		changed, err := store.TransitionRepairCase(ctx, c.ID, coderepair.StateInvestigating, 3,
			coderepair.StatePatchReady, repairTestEvent(c.ID, "patch_ready", time.Now().UTC()))
		if err != nil || !changed {
			return errors.New("could not record patch-ready result")
		}
		handled <- struct{}{}
		return nil
	}, nil, repairrunner.Config{Workers: 1, Lease: time.Second,
		MaxRun: 10 * time.Second, PollInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- r.Run(runCtx) }()
	defer func() { cancel(); <-done }()
	select {
	case <-handled:
	case <-time.After(10 * time.Second):
		t.Fatal("repair handler did not complete across multiple lease periods")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result, err := store.GetRepairJob(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if result.Status == coderepair.JobSucceeded {
			if result.Attempts != 1 {
				t.Fatalf("renewed job was reclaimed: attempts=%d", result.Attempts)
			}
			var status string
			if err := store.db.QueryRowContext(ctx, `SELECT status FROM repair_attempts WHERE id=$1`, attempt.ID).Scan(&status); err != nil || status != "succeeded" {
				t.Fatalf("completed attempt status=%q err=%v", status, err)
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
	restartedStore := reopenRepairStore(t, store)
	count, err := restartedStore.RecoverRepairJobs(ctx, now.Add(300*time.Millisecond))
	if err != nil || count != 1 {
		t.Fatalf("reconcile finished case: count=%d err=%v", count, err)
	}
	finalJob, err := restartedStore.GetRepairJob(ctx, claimed.ID)
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
