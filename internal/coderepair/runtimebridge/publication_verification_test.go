package runtimebridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/publisher"
	repairverify "github.com/Cyaside/Triovexa/internal/coderepair/verification"
	"github.com/Cyaside/Triovexa/internal/storage/postgres"
	"github.com/google/uuid"
)

// This downstream fixture starts with the real persisted native/Docker proof.
// GitHub, human review, rollout events and telemetry are all explicit synthetic
// fixtures; no repository, deployment pipeline or live workload is contacted.
func nativeProofPublicationAndVerification(t *testing.T, store *postgres.PostgresStore, caseID, checkoutRoot string) map[string]any {
	t.Helper()
	ctx := context.Background()
	repairCase, err := store.GetRepairCase(ctx, caseID)
	if err != nil || repairCase.State != coderepair.StatePatchReady {
		t.Fatal("downstream fixture requires the actual persisted native patch-ready case")
	}
	now := time.Now().UTC()
	digest, ok, err := store.PrepareRepairPublication(ctx, caseID, "fixture-reviewer", repairCase.Version, now)
	if err != nil || !ok {
		t.Fatalf("native publication review: ok=%v err=%v", ok, err)
	}
	repairCase, _ = store.GetRepairCase(ctx, caseID)
	if _, ok, err := store.ApproveRepairPublication(ctx, caseID, "fixture-reviewer", digest, repairCase.Version, now); err != nil || !ok {
		t.Fatalf("native publication approval: ok=%v err=%v", ok, err)
	}
	publication, err := store.ClaimRepairPublication(ctx, "fixture-publisher", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.GetRepairPublicationInput(ctx, publication)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(checkoutRoot, "internal", "workload", "worker.go"))
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(original), "version != 1", "version != 2", 1)
	github, remote := newLocalGitHubFixture(t, input.Binding, publication, input.Case.BaseSHA, string(original), changed)
	writesAllowed := 0
	service, err := publisher.NewService(github, func(_ context.Context, _ coderepair.RepositoryBinding, requestedSHA string) (string, func() error, error) {
		if requestedSHA != input.Case.BaseSHA {
			t.Fatal("publisher requested an unapproved checkout revision")
		}
		parent := t.TempDir()
		root := filepath.Join(parent, "checkout")
		fixtureGit(t, parent, "clone", "--no-hardlinks", "--no-checkout", "--", checkoutRoot, root)
		fixtureGit(t, root, "config", "--local", "core.autocrlf", "false")
		fixtureGit(t, root, "checkout", "--detach", requestedSHA)
		return root, func() error { return nil }, nil
	}, func(context.Context) error { writesAllowed++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	pr, head, err := service.Publish(ctx, publication, publisher.Input(input), now)
	if err != nil || pr.Number != 7 || head != remote.head {
		t.Fatalf("native proof could not be published to the fake GitHub: number=%d head=%s err=%v", pr.Number, head, err)
	}
	// A second delivery must reconcile the exact native patch and create no
	// extra branch/PR, even though both initial acknowledgements were lost.
	if repeated, repeatedHead, err := service.Publish(ctx, publication, publisher.Input(input), now); err != nil || repeated.Number != pr.Number || repeatedHead != head {
		t.Fatalf("native publication replay: err=%v", err)
	}
	if remote.branchWrites != 1 || remote.prWrites != 1 || writesAllowed != 1 {
		t.Fatal("native publication duplicated an external effect or authorization")
	}
	if ok, err := store.CompleteRepairPublication(ctx, publication, head, pr.Number, pr.URL, now); err != nil || !ok {
		t.Fatalf("native publication outcome: ok=%v err=%v", ok, err)
	}
	mergeSHA := strings.Repeat("b", 40)
	event := coderepair.PREvent{DeliveryID: uuid.NewString(), Repository: "Cyaside/Triovexa", Branch: publication.BranchName,
		BaseRef: input.Binding.BaseRef, HeadSHA: head, Number: pr.Number, Merged: true, MergeSHA: mergeSHA, ReceivedAt: now}
	if ok, err := store.ApplyRepairPREvent(ctx, event); err != nil || !ok {
		t.Fatalf("synthetic reviewed merge: ok=%v err=%v", ok, err)
	}
	if ok, err := store.ApplyRepairPREvent(ctx, event); err != nil || ok {
		t.Fatalf("duplicate merge changed state: ok=%v err=%v", ok, err)
	}
	// An anchored synthetic clock exercises ten-second observation windows
	// without waiting on a rollout. The HTTP client still checks real freshness.
	clock := time.Now().UTC().Add(-40 * time.Second)
	client, samples := newNativeTelemetryFixture(t, clock, input.Case.DeployedSHA, mergeSHA)
	baseline, err := client.Snapshot(ctx)
	if err != nil || baseline.WorkerHealthy || baseline.AlertCleared || baseline.QueueBacklog == 0 {
		t.Fatalf("synthetic pre-deploy baseline: err=%v", err)
	}
	deploymentID := uuid.NewString()
	if ok, err := store.StartRepairDeployment(ctx, caseID, deploymentID, input.Binding.Environment, head, baseline, clock); err == nil || ok {
		t.Fatal("unmerged PR head was accepted as a deployment revision")
	}
	if ok, err := store.StartRepairDeployment(ctx, caseID, deploymentID, input.Binding.Environment, mergeSHA, baseline, clock); err != nil || !ok {
		t.Fatalf("synthetic deployment start: ok=%v err=%v", ok, err)
	}
	if ok, err := store.CompleteRepairDeployment(ctx, caseID, deploymentID, input.Binding.Environment, mergeSHA, clock.Add(5*time.Second)); err != nil || !ok {
		t.Fatalf("synthetic deployment completion: ok=%v err=%v", ok, err)
	}
	deployment, err := store.ClaimRepairVerification(ctx, clock.Add(6*time.Second), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := store.FinishRepairVerification(ctx, deployment, true, "fixture requires measured observations", clock.Add(7*time.Second)); err == nil || ok {
		t.Fatal("native patch/PR was treated as recovery without telemetry")
	}
	for index := 0; index < 3; index++ {
		sample, err := client.Snapshot(ctx)
		at := clock.Add(time.Duration(10+10*index) * time.Second)
		passed := repairverify.Check(deployment.Baseline, sample, mergeSHA)
		if err != nil || !passed {
			t.Fatalf("synthetic recovery observation %d: passed=%v err=%v", index+1, passed, err)
		}
		if err := store.RecordRepairVerificationSample(ctx, deployment, sample, passed, at); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := store.FinishRepairVerification(ctx, deployment, true, "three spaced synthetic recovery observations", clock.Add(31*time.Second)); err != nil || !ok {
		t.Fatalf("native proof downstream recovery: ok=%v err=%v", ok, err)
	}
	final, err := store.GetRepairCase(ctx, caseID)
	if err != nil || final.State != coderepair.StateRecovered || samples.Load() != 4 {
		t.Fatalf("native proof downstream state=%s samples=%d err=%v", final.State, samples.Load(), err)
	}
	return map[string]any{"status": "passed", "case_state": final.State, "synthetic_github_branch_writes": remote.branchWrites,
		"synthetic_github_pr_writes": remote.prWrites, "synthetic_github_requests": remote.requests, "synthetic_recovery_observations": 3,
		"real_github_writes": 0, "real_deployments": 0, "actual_ci_executed": false, "human_approval_source": "synthetic-fixture"}
}

func newNativeTelemetryFixture(t *testing.T, clock time.Time, oldSHA, mergeSHA string) (*repairverify.WorkloadClient, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		respond := func(value any) { _ = json.NewEncoder(writer).Encode(value) }
		switch request.URL.Path {
		case "/state":
			if request.Header.Get("Authorization") != "Bearer synthetic-control-key" {
				t.Error("synthetic telemetry credential changed")
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			index := int(calls.Add(1)) - 1
			sample := repairverify.Sample{Target: "queue-worker", Source: "redis-streams", Timestamp: clock.Add(-time.Second),
				Complete: true, QueueBacklog: 100, JobsProcessed: 10, Errors: 1, DeployedRevision: oldSHA}
			if index > 0 {
				sample.Timestamp, sample.WorkerHealthy, sample.DeployedRevision = clock.Add(time.Duration(index*10)*time.Second), true, mergeSHA
				sample.QueueBacklog, sample.JobsProcessed = int64(100-index*20), int64(10+index*20)
			}
			respond(sample)
		case "/api/v1/rules":
			respond(map[string]any{"status": "success", "data": map[string]any{"groups": []any{map[string]any{"rules": []any{map[string]string{"name": "TriovexaQueueBacklogHigh", "type": "alerting", "health": "ok"}}}}}})
		case "/api/v1/alerts":
			alerts := []any{}
			if calls.Load() <= 1 {
				alerts = append(alerts, map[string]any{"labels": map[string]string{"alertname": "TriovexaQueueBacklogHigh"}, "state": "firing"})
			}
			respond(map[string]any{"status": "success", "data": map[string]any{"alerts": alerts}})
		default:
			t.Error("unplanned synthetic telemetry route")
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client, err := repairverify.NewWorkloadClient(server.URL, "synthetic-control-key", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return client, &calls
}
