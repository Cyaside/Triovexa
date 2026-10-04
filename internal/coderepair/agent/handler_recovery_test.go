package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
	"github.com/Cyaside/Triovexa/internal/domain"
)

type handlerRecoveryStore struct {
	repairCase coderepair.Case
	attempt    coderepair.Attempt
	binding    coderepair.RepositoryBinding
	snapshot   coderepair.EvidenceSnapshot
	outcome    coderepair.InvestigationOutcome
	records    int
}

func (s *handlerRecoveryStore) GetRepairCase(context.Context, string) (coderepair.Case, error) {
	return s.repairCase, nil
}
func (s *handlerRecoveryStore) GetRepairAttempt(context.Context, string) (coderepair.Attempt, error) {
	return s.attempt, nil
}
func (s *handlerRecoveryStore) GetRepositoryBinding(context.Context, string) (coderepair.RepositoryBinding, error) {
	return s.binding, nil
}
func (s *handlerRecoveryStore) GetRepairEvidenceSnapshot(context.Context, string) (coderepair.EvidenceSnapshot, error) {
	return s.snapshot, nil
}
func (s *handlerRecoveryStore) RecordRepairInvestigationOutcome(_ context.Context, outcome coderepair.InvestigationOutcome) (bool, error) {
	s.records++
	s.outcome = outcome
	return true, nil
}

type captureRecoveryClaim struct {
	calls int
	claim ClaimedInvestigation
}

func (s *captureRecoveryClaim) Investigate(ctx context.Context, _ *sandbox.Workspace, _ coderepair.RepositoryBinding,
	_ coderepair.EvidenceSnapshot, selection coderepair.AgentSelection, _ string) InvestigationResult {
	s.calls++
	s.claim, _ = InvestigationClaim(ctx)
	// This boundary fixture does not authorize a patch. Native Engine/Runner
	// tests separately prove the actual sealed terminal-proof reconciliation.
	return InvestigationResult{Status: coderepair.StateBlocked, Code: "RECOVERY_BOUNDARY_FIXTURE",
		Provider: selection.Provider, Model: selection.Model, Prompt: selection.PromptVersion}
}

func TestHandlerKeepsExpiredEvidenceRecoveryWithinUnchangedScope(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		corrupt    func(*handlerRecoveryStore)
		wantEngine bool
		wantCode   string
	}{
		{"expired approved snapshot", func(*handlerRecoveryStore) {}, true, "RECOVERY_BOUNDARY_FIXTURE"},
		{"invalid snapshot digest", func(store *handlerRecoveryStore) { store.snapshot.SHA256 = strings.Repeat("0", 64) }, false, "EVIDENCE_STALE"},
		{"changed approval scope", func(store *handlerRecoveryStore) { store.repairCase.ScopeDigest = strings.Repeat("0", 64) }, false, "SCOPE_CHANGED"},
		{"disabled repository binding", func(store *handlerRecoveryStore) { store.binding.Enabled = false }, false, "EVIDENCE_STALE"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			_, binding, initial, root := loopFixture(t)
			past := time.Now().UTC().Add(-2 * time.Minute)
			incident := domain.Incident{ID: initial.IncidentID, ServiceName: binding.ServiceName, Environment: binding.Environment}
			snapshot, err := coderepair.BuildEvidenceSnapshot(incident, []domain.EvidenceItem{
				{ID: "log-1", IncidentID: incident.ID, Type: "log", Source: "fixture-logs", Timestamp: past, Snippet: "schema two jobs failed"},
				{ID: "metric-1", IncidentID: incident.ID, Type: "metric", Source: "workload-control", Timestamp: past, Snippet: "backlog rising",
					MetadataJSON: `{"target":"queue-worker","complete":true,"deployed_revision":"` + initial.DeployedRevision + `"}`},
			}, past, coderepair.EvidenceLimits{MaxItems: 4, MaxSnippetBytes: 1024, MaxTotalBytes: 2048, MaxAge: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			repairCase := coderepair.Case{ID: "case-1", IncidentID: incident.ID, BindingID: binding.ID,
				BaseSHA: strings.TrimSpace(runLoopGit(t, root, "rev-parse", "HEAD")), DeployedSHA: snapshot.DeployedRevision,
				PolicyVersion: binding.PolicyVersion, State: coderepair.StateInvestigating, Version: 2}
			repairCase.ScopeDigest, err = coderepair.ScopeDigest(repairCase, binding)
			if err != nil {
				t.Fatal(err)
			}
			store := &handlerRecoveryStore{repairCase: repairCase, binding: binding, snapshot: snapshot,
				attempt: coderepair.Attempt{ID: "attempt-1", CaseID: repairCase.ID, Status: coderepair.JobRunning,
					Provider: "openai-compatible", Model: "fixture-model", PromptVersion: PromptVersion}}
			scenario.corrupt(store)
			engine := &captureRecoveryClaim{}
			checkouts := 0
			handler, err := NewHandler(store, engine, func(context.Context, coderepair.RepositoryBinding, string) (string, func() error, error) {
				checkouts++
				return root, func() error { return nil }, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			payload, _ := json.Marshal(map[string]any{"case_id": repairCase.ID, "attempt_id": store.attempt.ID, "expected_version": int64(2)})
			job := coderepair.Job{ID: "job-1", CaseID: repairCase.ID, AttemptID: store.attempt.ID, LeaseToken: "lease-1",
				Status: coderepair.JobRunning, Type: coderepair.JobTypeInvestigation, PayloadJSON: string(payload)}
			if err := handler.Handle(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			if store.records != 1 || store.outcome.ErrorCode != scenario.wantCode {
				t.Fatalf("handler result not safely recorded: records=%d code=%s", store.records, store.outcome.ErrorCode)
			}
			if scenario.wantEngine {
				if engine.calls != 1 || !engine.claim.RecoveryOnly || checkouts != 1 {
					t.Fatalf("expired scope did not reach recovery-only engine: calls=%d recovery=%v checkouts=%d", engine.calls, engine.claim.RecoveryOnly, checkouts)
				}
			} else if engine.calls != 0 || checkouts != 0 {
				t.Fatal("invalid snapshot/scope reached a recovery engine or checkout")
			}
		})
	}
}
