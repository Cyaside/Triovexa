package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	repairverify "github.com/Cyaside/Triovexa/internal/coderepair/verification"
)

func TestRepairDeploymentRequiresMergeRevisionAndThreeObservationsIntegration(t *testing.T) {
	store := isolatedRepairStore(t)
	ctx := context.Background()
	c := readyPublicationFixture(t, store)
	digest, ok, err := store.PrepareRepairPublication(ctx, c.ID, "operator", c.Version, time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("prepare: %v %v", ok, err)
	}
	c, _ = store.GetRepairCase(ctx, c.ID)
	_, ok, err = store.ApproveRepairPublication(ctx, c.ID, "operator", digest, c.Version, time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("approve: %v %v", ok, err)
	}
	p, err := store.ClaimRepairPublication(ctx, "publisher", time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	head := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	merge := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if ok, err := store.CompleteRepairPublication(ctx, p, head, 10, "https://github.com/Cyaside/Triovexa/pull/10", time.Now().UTC()); err != nil || !ok {
		t.Fatalf("PR: %v %v", ok, err)
	}
	if ok, err := store.ApplyRepairPREvent(ctx, RepairPREvent{DeliveryID: uuid.NewString(), Repository: "Cyaside/Triovexa",
		Branch: p.BranchName, BaseRef: "repair-code-workflow", HeadSHA: head, Number: 10,
		Merged: true, MergeSHA: merge, ReceivedAt: time.Now().UTC()}); err != nil || !ok {
		t.Fatalf("merge: %v %v", ok, err)
	}
	clock := time.Now().UTC().Add(-40 * time.Second)
	baseline := repairverify.Sample{Target: "queue-worker", Source: "redis-streams", Timestamp: clock.Add(-10 * time.Second),
		Complete: true, AlertObserved: true, QueueBacklog: 100, JobsProcessed: 0, Errors: 1,
		DeployedRevision: "1111111111111111111111111111111111111111"}
	deploymentID := uuid.NewString()
	if ok, err := store.StartRepairDeployment(ctx, c.ID, deploymentID, "staging", head, baseline, clock); err == nil || ok {
		t.Fatalf("unmerged head accepted for deploy: %v %v", ok, err)
	}
	if ok, err := store.StartRepairDeployment(ctx, c.ID, deploymentID, "staging", merge, baseline, clock); err != nil || !ok {
		t.Fatalf("start: %v %v", ok, err)
	}
	if ok, err := store.CompleteRepairDeployment(ctx, c.ID, deploymentID, "production", merge, clock.Add(5*time.Second)); err == nil || ok {
		t.Fatalf("wrong environment accepted: %v %v", ok, err)
	}
	if ok, err := store.CompleteRepairDeployment(ctx, c.ID, deploymentID, "staging", merge, clock.Add(5*time.Second)); err != nil || !ok {
		t.Fatalf("complete: %v %v", ok, err)
	}
	d, err := store.ClaimRepairVerification(ctx, clock.Add(6*time.Second), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := store.FinishRepairVerification(ctx, d, true, "three samples", clock.Add(7*time.Second)); err == nil || ok {
		t.Fatalf("recovery without samples accepted: %v %v", ok, err)
	}
	for i := 0; i < 3; i++ {
		at := clock.Add(time.Duration(10+i*10) * time.Second)
		sample := repairverify.Sample{Target: "queue-worker", Source: "redis-streams", Timestamp: at,
			Complete: true, AlertObserved: true, AlertCleared: true, WorkerHealthy: true, QueueBacklog: int64(80 - i*20), JobsProcessed: int64(20 + i*20),
			Errors: 1, DeployedRevision: merge}
		if err := store.RecordRepairVerificationSample(ctx, d, sample, true, at); err != nil {
			t.Fatalf("sample %d: %v", i, err)
		}
	}
	if ok, err := store.FinishRepairVerification(ctx, d, true, "three samples", clock.Add(31*time.Second)); err != nil || !ok {
		t.Fatalf("recovery: %v %v", ok, err)
	}
	c, err = store.GetRepairCase(ctx, c.ID)
	if err != nil || c.State != coderepair.StateRecovered {
		t.Fatalf("case state: %+v %v", c, err)
	}
}

func TestRepairDeploymentMissingTelemetryRemainsInconclusiveIntegration(t *testing.T) {
	store := isolatedRepairStore(t)
	ctx := context.Background()
	c := readyPublicationFixture(t, store)
	now := time.Now().UTC()
	digest, ok, err := store.PrepareRepairPublication(ctx, c.ID, "operator", c.Version, now)
	if err != nil || !ok {
		t.Fatalf("prepare: %v %v", ok, err)
	}
	c, _ = store.GetRepairCase(ctx, c.ID)
	_, ok, err = store.ApproveRepairPublication(ctx, c.ID, "operator", digest, c.Version, now)
	if err != nil || !ok {
		t.Fatalf("approve: %v %v", ok, err)
	}
	p, err := store.ClaimRepairPublication(ctx, "publisher", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	head := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	merge := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if ok, err := store.CompleteRepairPublication(ctx, p, head, 11, "https://github.com/Cyaside/Triovexa/pull/11", now); err != nil || !ok {
		t.Fatalf("PR: %v %v", ok, err)
	}
	if ok, err := store.ApplyRepairPREvent(ctx, RepairPREvent{DeliveryID: uuid.NewString(), Repository: "Cyaside/Triovexa",
		Branch: p.BranchName, BaseRef: "repair-code-workflow", HeadSHA: head, Number: 11,
		Merged: true, MergeSHA: merge, ReceivedAt: now}); err != nil || !ok {
		t.Fatalf("merge: %v %v", ok, err)
	}
	start := now.Add(-20 * time.Second)
	baseline := repairverify.Sample{Target: "queue-worker", Source: "redis-streams", Timestamp: start.Add(-time.Second),
		Complete: true, AlertObserved: true, QueueBacklog: 100, JobsProcessed: 0, Errors: 1,
		DeployedRevision: "1111111111111111111111111111111111111111"}
	deploymentID := uuid.NewString()
	if ok, err := store.StartRepairDeployment(ctx, c.ID, deploymentID, "staging", merge, baseline, start); err != nil || !ok {
		t.Fatalf("start: %v %v", ok, err)
	}
	if ok, err := store.CompleteRepairDeployment(ctx, c.ID, deploymentID, "staging", merge, start.Add(time.Second)); err != nil || !ok {
		t.Fatalf("complete: %v %v", ok, err)
	}
	d, err := store.ClaimRepairVerification(ctx, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := store.FinishRepairVerification(ctx, d, false, "telemetry missing or stale", now.Add(time.Second)); err != nil || !ok {
		t.Fatalf("inconclusive: %v %v", ok, err)
	}
	c, err = store.GetRepairCase(ctx, c.ID)
	if err != nil || c.State != coderepair.StateInconclusive {
		t.Fatalf("missing telemetry was not inconclusive: %+v %v", c, err)
	}
}
