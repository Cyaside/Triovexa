package runtimebridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/ai/admission"
	"github.com/Cyaside/Triovexa/internal/ai/modelgateway"
	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/agent"
)

type completedProofStore struct {
	*claimFixture
	*toolReceiptFixtureStore
}

func TestExpiredEvidenceAllowsOnlyCompletedReceiptRecovery(t *testing.T) {
	for _, completed := range []bool{false, true} {
		name := "unfinished attempt cannot dispatch"
		if completed {
			name = "completed proof can reconcile"
		}
		t.Run(name, func(t *testing.T) {
			h := newReceiptHarness(t)
			first := h.session(t, true)
			first.Execute(context.Background(), sourceRead("read-1"))
			if completed {
				h.propose(t, first)
				fixtureGit(t, h.w.RootPath(), "checkout", "--", "internal/workload/worker.go")
			}
			h.claim.Job.Status = coderepair.JobRunning
			h.claim.Job.LeaseUntil = time.Now().Add(time.Minute)
			h.claim.Case = coderepair.Case{ID: h.claim.Job.CaseID, BaseSHA: h.base, State: coderepair.StateInvestigating, Version: h.claim.ExpectedVersion}
			h.claim.Attempt = coderepair.Attempt{ID: h.claim.Job.AttemptID, CaseID: h.claim.Job.CaseID}
			// Handler sets this only after validating the unchanged approval scope,
			// binding and snapshot digest. It grants reconciliation, never dispatch.
			h.claim.RecoveryOnly = true
			store := &claimFixture{job: h.claim.Job, caseRecord: h.claim.Case}
			gatewayCalls := 0
			engine := &Engine{Process: Process{Version: EngineVersion}, Tests: h.tests, Store: store,
				Profile: "offline-fixture", ConfigVersion: "fixture-v1", Limits: DefaultLimits(), Receipts: h.store, Cipher: h.cipher}
			engine.Gateway = func(context.Context, agent.ClaimedInvestigation, Fence) (Transport, func(), error) {
				gatewayCalls++
				return Transport{}, nil, errors.New("transport must not open during expired-evidence recovery")
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			beforeTests := h.tests.calls
			result := engine.Investigate(agent.WithClaimedInvestigation(ctx, h.claim), h.w, h.binding, h.snapshot,
				coderepair.AgentSelection{Provider: "openai-compatible", Model: "fixture-model", PromptVersion: PromptVersion}, "go-test-workload")
			if gatewayCalls != 0 || h.tests.calls != beforeTests {
				t.Fatalf("expired evidence dispatched new effects: gateway=%d tests=%d", gatewayCalls, h.tests.calls-beforeTests)
			}
			if completed {
				if result.Status != coderepair.StatePatchReady || result.PatchReport.SHA256 != first.Result().PatchReport.SHA256 {
					t.Fatalf("completed proof was lost on recovery: status=%s code=%s", result.Status, result.Code)
				}
			} else if result.Status != coderepair.StateBlocked || result.Code != "EVIDENCE_STALE" || result.Patch != "" || result.PatchReport.SHA256 != "" {
				t.Fatalf("unfinished attempt escaped freshness gate: status=%s code=%s", result.Status, result.Code)
			}
		})
	}
}

func TestNativeRunnerRecoversProofWithoutChildOrCheckpointDatabase(t *testing.T) {
	h := newReceiptHarness(t)
	first := h.session(t, true)
	first.Execute(context.Background(), sourceRead("read-1"))
	h.propose(t, first)
	fixtureGit(t, h.w.RootPath(), "checkout", "--", "internal/workload/worker.go")
	h.claim.Job.Status = coderepair.JobRunning
	h.claim.Job.LeaseUntil = time.Now().Add(time.Minute)
	h.claim.Case = coderepair.Case{ID: h.claim.Job.CaseID, BaseSHA: h.base, State: coderepair.StateInvestigating, Version: h.claim.ExpectedVersion}
	h.claim.RecoveryOnly = true
	provider := ai.ProviderConfig{Name: "openai-compatible", BaseURL: "http://127.0.0.1:1", AllowHTTP: true,
		Model: "fixture-model", APIKey: "synthetic-unused-provider-key", Timeout: time.Second}
	client, err := ai.NewOpenAICompatibleClient(provider)
	if err != nil {
		t.Fatal(err)
	}
	budget := modelgateway.BudgetConfig{Campaign: admission.Campaign{ID: "recovery-offline", Profile: "offline-fixture", Offline: true,
		MaxRequests: 6, MaxSpendMicroUSD: 200000, MaxInputTokens: 200000},
		Pricing:       admission.Pricing{Version: "synthetic-v1", Provider: provider.Name, Model: provider.Model},
		ConfigVersion: "fixture-v1", MaxInputTokens: 64000, MaxOutputTokens: 1500}
	selection, err := SealSelection(client, budget, h.cipher)
	if err != nil {
		t.Fatal(err)
	}
	selection.Runtime.ThreadID = h.claim.Job.CaseID + ":" + h.claim.Job.AttemptID
	h.claim.Attempt = coderepair.Attempt{ID: h.claim.Job.AttemptID, CaseID: h.claim.Job.CaseID,
		Provider: selection.Provider, Model: selection.Model, PromptVersion: selection.PromptVersion, Runtime: selection.Runtime}
	ledger, err := admission.NewService(admission.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	store := &completedProofStore{claimFixture: &claimFixture{job: h.claim.Job, caseRecord: h.claim.Case}, toolReceiptFixtureStore: h.store}
	// These unavailable transports must not matter after a completed proof is
	// restored. No child can launch, no checkpoint connection or HTTP can open.
	runner := &NativeRunner{Process: Process{Version: EngineVersion}, Tests: h.tests, Store: store, Ledger: ledger, Cipher: h.cipher}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	beforeTests := h.tests.calls
	result := runner.Investigate(agent.WithClaimedInvestigation(ctx, h.claim), h.w, h.binding, h.snapshot, selection, "go-test-workload")
	if result.Status != coderepair.StatePatchReady || result.PatchReport.SHA256 != first.Result().PatchReport.SHA256 || h.tests.calls != beforeTests || result.Runtime == nil || result.Runtime.Accounting.ModelRequests != 0 {
		t.Fatalf("native terminal reconciliation required a new transport: status=%s code=%s tests=%d runtime=%+v", result.Status, result.Code, h.tests.calls-beforeTests, result.Runtime)
	}
}
