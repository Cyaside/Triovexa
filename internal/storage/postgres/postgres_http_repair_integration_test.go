package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	repairverify "github.com/Cyaside/Triovexa/internal/coderepair/verification"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/google/uuid"
)

func TestHTTPRepairEvidenceApprovalDeploymentAndRecoveryIntegration(t *testing.T) {
	for _, name := range []string{"three healthy observations", "missing telemetry", "revoked binding"} {
		recoverService := name != "missing telemetry"
		t.Run(name, func(t *testing.T) {
			store := isolatedRepairStore(t)
			ctx := t.Context()
			now := time.Now().UTC()
			oldSHA := strings.Repeat("1", 40)
			merge := strings.Repeat("b", 40)
			incident := domain.Incident{ID: uuid.NewString(), ExternalAlertID: uuid.NewString(), AlertSource: "prometheus", Title: "Health endpoint returns HTTP 500", ServiceName: "api", Environment: "staging", Severity: "high", State: domain.IncidentStateEscalated, CreatedAt: now, UpdatedAt: now}
			if err := store.CreateIncident(ctx, incident); err != nil {
				t.Fatal(err)
			}
			profile := &coderepair.TelemetryProfile{Version: coderepair.TelemetryVersion, Kind: "http", AlertName: "HTTPServerErrors", LogSources: []string{"grafana-loki"}, HTTP: &coderepair.HTTPSignals{RequestsMetric: "http_requests_total", HealthMetric: "probe_success", RevisionMetric: "app_build_info", RevisionLabel: "revision", StatusLabel: "status", ServiceLabel: "service", EnvironmentLabel: "environment", WindowSeconds: 30, MinimumRequests: 1, MaxErrorRatio: 0.01}}
			binding := coderepair.RepositoryBinding{ID: uuid.NewString(), ServiceName: "api", Environment: "staging", RepositoryURL: "https://github.com/acme/api", BaseRef: "main", AllowedPaths: []string{"src"}, TestRecipes: []string{"go-test-workload"}, PolicyVersion: "repair-v3", TelemetryProfile: profile, Enabled: true, CreatedAt: now, UpdatedAt: now}
			if err := store.CreateRepositoryBinding(ctx, binding); err != nil {
				t.Fatal(err)
			}
			binding, err := store.GetRepositoryBinding(ctx, binding.ID)
			if err != nil || binding.TelemetryContract().Digest() == "" {
				t.Fatalf("profile round trip: %v", err)
			}
			baseline := repairverify.Sample{Target: "api", Environment: "staging", Source: "prometheus-http-v1", Timestamp: now, ProfileSHA256: binding.TelemetryContract().Digest(), Complete: true, AlertObserved: true, DeployedRevision: oldSHA, HTTP: &repairverify.HTTPObservation{Healthy: false, Requests: 100, Errors: 100}}
			meta, _ := json.Marshal(baseline)
			metric := domain.EvidenceItem{ID: uuid.NewString(), IncidentID: incident.ID, Type: "metric", Source: "repair-prometheus", Snippet: "HTTP 500 responses", Timestamp: now, MetadataJSON: string(meta)}
			log := domain.EvidenceItem{ID: uuid.NewString(), IncidentID: incident.ID, Type: "log", Source: "grafana-loki", Snippet: "RuntimeError in healthz", Timestamp: now, MetadataJSON: "{}"}
			for _, item := range []domain.EvidenceItem{metric, log} {
				if err := store.SaveEvidenceItems(ctx, []domain.EvidenceItem{item}); err != nil {
					t.Fatal(err)
				}
			}
			proposal, err := coderepair.NewProposalService(store, func(_ context.Context, _ coderepair.RepositoryBinding) (string, error) { return oldSHA, nil }, coderepair.EvidenceLimits{MaxItems: 10, MaxSnippetBytes: 1024, MaxTotalBytes: 2048, MaxAge: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			c, snapshot, err := proposal.Propose(ctx, incident.ID, "operator", now)
			if err != nil || snapshot.TelemetrySHA256 != baseline.ProfileSHA256 {
				t.Fatalf("HTTP proposal: %v", err)
			}
			auth, _ := coderepair.NewInvestigationAuthorizationService(store)
			if _, err := store.db.ExecContext(ctx, `UPDATE evidence_items SET source='untrusted-webhook' WHERE id=$1`, metric.ID); err != nil {
				t.Fatal(err)
			}
			selection := coderepair.AgentSelection{Provider: "offline", Model: "fixture", PromptVersion: "fixture-v1"}
			if _, _, _, err := auth.Authorize(ctx, c.ID, "operator", selection, now); err == nil {
				t.Fatal("approval accepted substituted revision provenance")
			}
			if _, err := store.db.ExecContext(ctx, `UPDATE evidence_items SET source='repair-prometheus' WHERE id=$1`, metric.ID); err != nil {
				t.Fatal(err)
			}
			_, attempt, _, err := auth.Authorize(ctx, c.ID, "operator", selection, now)
			if err != nil {
				t.Fatal(err)
			}
			job, err := store.ClaimRepairJob(ctx, "fixture", now, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			c, _ = store.GetRepairCase(ctx, c.ID)
			// Runner/sandbox proof is exercised separately. This integration owns
			// persistence, authorization, merge identity and recovery authority.
			patch := []byte("diff --git a/src/app.go b/src/app.go\nfixture patch\n")
			sum := sha256.Sum256(patch)
			digest := hex.EncodeToString(sum[:])
			report, _ := json.Marshal(map[string]any{"status": coderepair.StatePatchReady, "recipe_id": "go-test-workload", "patch_sha256": digest, "before_exit": 1, "after_exit": 0, "evidence_ids": []string{metric.ID, log.ID}})
			ok, err := store.RecordRepairInvestigationOutcome(ctx, coderepair.InvestigationOutcome{JobID: job.ID, LeaseToken: job.LeaseToken, CaseID: c.ID, AttemptID: attempt.ID, ExpectedVersion: c.Version, State: coderepair.StatePatchReady, Patch: patch, PatchSHA256: digest, ReportJSON: report, RecordedAt: now})
			if err != nil || !ok {
				t.Fatalf("outcome: %v", err)
			}
			if _, err := store.CompleteRepairJob(ctx, job.ID, job.LeaseToken, now); err != nil {
				t.Fatal(err)
			}
			c, _ = store.GetRepairCase(ctx, c.ID)
			digest, ok, err = store.PrepareRepairPublication(ctx, c.ID, "operator", c.Version, now)
			if err != nil || !ok {
				t.Fatalf("publication review: %v", err)
			}
			c, _ = store.GetRepairCase(ctx, c.ID)
			if _, ok, err := store.ApproveRepairPublication(ctx, c.ID, "operator", digest, c.Version, now); err != nil || !ok {
				t.Fatalf("publication approval: %v", err)
			}
			publication, err := store.ClaimRepairPublication(ctx, "publisher", now, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			head := strings.Repeat("a", 40)
			if ok, err := store.CompleteRepairPublication(ctx, publication, head, 42, "https://github.com/acme/api/pull/42", now); err != nil || !ok {
				t.Fatalf("publication: %v", err)
			}
			if ok, err := store.ApplyRepairPREvent(ctx, coderepair.PREvent{DeliveryID: uuid.NewString(), Repository: "acme/api", Branch: publication.BranchName, BaseRef: "main", HeadSHA: head, Number: 42, Merged: true, MergeSHA: merge, ReceivedAt: now}); err != nil || !ok {
				t.Fatalf("merge: %v", err)
			}
			clock := time.Now().UTC().Add(-40 * time.Second)
			baseline.Timestamp = clock.Add(-time.Second)
			deploymentID := uuid.NewString()
			if ok, err := store.StartRepairDeployment(ctx, c.ID, deploymentID, "staging", merge, baseline, clock); err != nil || !ok {
				t.Fatalf("HTTP deployment start: %v", err)
			}
			if ok, err := store.CompleteRepairDeployment(ctx, c.ID, deploymentID, "staging", merge, clock.Add(time.Second)); err != nil || !ok {
				t.Fatalf("deployment complete: %v", err)
			}
			reopened := reopenRepairStore(t, store)
			d, err := reopened.ClaimRepairVerification(ctx, clock.Add(2*time.Second), time.Minute)
			if err != nil || d.Contract.Digest() != baseline.ProfileSHA256 {
				t.Fatalf("pinned contract/restart: %v", err)
			}
			if ok, err := reopened.FinishRepairVerification(ctx, d, true, "no samples", clock.Add(3*time.Second)); err == nil || ok {
				t.Fatal("recovered without observations")
			}
			if recoverService {
				for i := 0; i < 3; i++ {
					at := clock.Add(time.Duration(10+i*10) * time.Second)
					sample := baseline
					sample.Timestamp = at
					sample.DeployedRevision = merge
					sample.AlertCleared = true
					sample.HTTP = &repairverify.HTTPObservation{Healthy: true, Requests: 100, Errors: 0}
					wrong := sample
					wrong.Environment = "production"
					if err := reopened.RecordRepairVerificationSample(ctx, d, wrong, true, at); err == nil {
						t.Fatal("cross-environment observation accepted")
					}
					if err := reopened.RecordRepairVerificationSample(ctx, d, sample, true, at); err != nil {
						t.Fatal(err)
					}
				}
			}
			if name == "revoked binding" {
				if _, err := reopened.DisableRepositoryBinding(ctx, binding.ID); err != nil {
					t.Fatal(err)
				}
				if ok, err := reopened.FinishRepairVerification(ctx, d, true, "revoked", clock.Add(31*time.Second)); err == nil || ok {
					t.Fatal("revoked binding allowed recovery")
				}
				recoverService = false
			}
			if ok, err := reopened.FinishRepairVerification(ctx, d, recoverService, "fixture observation result", clock.Add(31*time.Second)); err != nil || !ok {
				t.Fatalf("finish: %v", err)
			}
			c, _ = reopened.GetRepairCase(ctx, c.ID)
			expected := coderepair.StateInconclusive
			if recoverService {
				expected = coderepair.StateRecovered
			}
			if c.State != expected {
				t.Fatalf("case state %s, want %s", c.State, expected)
			}
		})
	}
}
