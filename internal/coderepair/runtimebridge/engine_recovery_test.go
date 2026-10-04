package runtimebridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/agent"
)

func TestEngineRestoresTerminalProofWithoutTransportOrTests(t *testing.T) {
	h := newReceiptHarness(t)
	first := h.session(t, true)
	first.Execute(context.Background(), sourceRead("read-1"))
	h.propose(t, first)
	// A restart receives a fresh private checkout at the approved base SHA.
	fixtureGit(t, h.w.RootPath(), "checkout", "--", "internal/workload/worker.go")
	h.claim.Job.Status = coderepair.JobRunning
	h.claim.Job.LeaseUntil = time.Now().Add(time.Minute)
	h.claim.Case = coderepair.Case{ID: h.claim.Job.CaseID, BaseSHA: h.base, State: coderepair.StateInvestigating, Version: h.claim.ExpectedVersion}
	h.claim.Attempt = coderepair.Attempt{ID: h.claim.Job.AttemptID, CaseID: h.claim.Job.CaseID}
	store := &claimFixture{job: h.claim.Job, caseRecord: h.claim.Case}
	gatewayCalls := 0
	engine := &Engine{Process: Process{Version: EngineVersion}, Tests: h.tests, Store: store,
		Profile: "offline-fixture", ConfigVersion: "fixture-v1", Limits: DefaultLimits(), Receipts: h.store, Cipher: h.cipher}
	engine.Gateway = func(context.Context, agent.ClaimedInvestigation, Fence) (Transport, func(), error) {
		gatewayCalls++
		return Transport{}, nil, errors.New("transport intentionally unavailable after crash")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ctx = agent.WithClaimedInvestigation(ctx, h.claim)
	beforeTests := h.tests.calls
	result := engine.Investigate(ctx, h.w, h.binding, h.snapshot,
		coderepair.AgentSelection{Provider: "openai-compatible", Model: "fixture-model", PromptVersion: PromptVersion}, "go-test-workload")
	if result.Status != coderepair.StatePatchReady || result.PatchReport.SHA256 != first.Result().PatchReport.SHA256 || gatewayCalls != 0 || h.tests.calls != beforeTests {
		t.Fatalf("terminal proof replay invoked new effects: status=%s code=%s gateway=%d tests=%d", result.Status, result.Code, gatewayCalls, h.tests.calls-beforeTests)
	}
}
