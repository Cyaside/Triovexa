package runtimebridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/agent"
)

func TestDurableBaselineReplaysBeforeModelWithoutAnotherTest(t *testing.T) {
	h := newReceiptHarness(t)
	newSession := func() *ToolSession {
		t.Helper()
		s, err := NewToolSession(h.w, h.binding, h.snapshot,
			coderepair.AgentSelection{Provider: "openai-compatible", Model: "fixture-model", PromptVersion: PromptVersion},
			"go-test-workload", h.tests, func(context.Context) error { return nil }, DefaultLimits(), h.base)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.RestoreReceipts(context.Background(), h.store, h.claim, h.cipher); err != nil {
			t.Fatal(err)
		}
		return s
	}
	first := newSession()
	baseline, err := first.Baseline(context.Background())
	if err != nil || h.tests.calls != 1 || len(h.store.rows) != 1 || h.store.rows[0].Name != "baseline" || h.store.rows[0].State != "completed" {
		t.Fatalf("baseline was not durable before inference: tests=%d err=%v", h.tests.calls, err)
	}
	restarted := newSession()
	replayed, err := restarted.Baseline(context.Background())
	if err != nil || replayed != baseline || h.tests.calls != 1 || restarted.Result().Before != first.Result().Before {
		t.Fatalf("restart changed the original baseline or reran tests: tests=%d err=%v", h.tests.calls, err)
	}
	restarted.Execute(context.Background(), ToolRequest{CallID: "model-baseline", Name: "baseline", Args: []byte(`{}`)})
	if restarted.Result().Code != "PROVIDER_CONTRACT_INVALID" || h.tests.calls != 1 {
		t.Fatal("the model gained access to the Go-private baseline operation")
	}
}

func TestEngineBlocksInferenceUntilBaselineReceiptCommit(t *testing.T) {
	h := newReceiptHarness(t)
	h.store.completeErr = errors.New("fixture persistence unavailable")
	h.claim.Job.Status, h.claim.Job.LeaseUntil = coderepair.JobRunning, time.Now().Add(time.Minute)
	h.claim.Case = coderepair.Case{ID: h.claim.Job.CaseID, BaseSHA: h.base, State: coderepair.StateInvestigating, Version: h.claim.ExpectedVersion}
	h.claim.Attempt = coderepair.Attempt{ID: h.claim.Job.AttemptID, CaseID: h.claim.Job.CaseID}
	store := &claimFixture{job: h.claim.Job, caseRecord: h.claim.Case}
	gatewayCalls := 0
	engine := &Engine{Process: Process{Version: EngineVersion}, Tests: h.tests, Store: store,
		Profile: "offline-fixture", ConfigVersion: "fixture-v1", Limits: DefaultLimits(), Receipts: h.store, Cipher: h.cipher,
		Gateway: func(context.Context, agent.ClaimedInvestigation, Fence) (Transport, func(), error) {
			gatewayCalls++
			return Transport{}, nil, errors.New("must not dispatch before durable baseline")
		}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	result := engine.Investigate(agent.WithClaimedInvestigation(ctx, h.claim), h.w, h.binding, h.snapshot,
		coderepair.AgentSelection{Provider: "openai-compatible", Model: "fixture-model", PromptVersion: PromptVersion}, "go-test-workload")
	if result.Code != "TOOL_DISPATCH_UNCERTAIN" || result.Status != coderepair.StateBlocked || gatewayCalls != 0 || h.tests.calls != 1 || len(h.store.rows) != 1 || h.store.rows[0].State != "started" {
		t.Fatalf("inference escaped baseline commit boundary: code=%s gateway=%d tests=%d", result.Code, gatewayCalls, h.tests.calls)
	}
	result = engine.Investigate(agent.WithClaimedInvestigation(ctx, h.claim), h.w, h.binding, h.snapshot,
		coderepair.AgentSelection{Provider: "openai-compatible", Model: "fixture-model", PromptVersion: PromptVersion}, "go-test-workload")
	if result.Code != "CHECKPOINT_INCOMPATIBLE" || gatewayCalls != 0 || h.tests.calls != 1 {
		t.Fatal("an uncertain baseline was silently rerun after restart")
	}
}
