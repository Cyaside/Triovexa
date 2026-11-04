package runtimebridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/agent"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
)

type cutoverBaselineTester struct {
	result sandbox.TestResult
	err    error
	calls  int
}

func (s *cutoverBaselineTester) Run(context.Context, string, coderepair.RepositoryBinding, string) (sandbox.TestResult, error) {
	s.calls++
	return s.result, s.err
}

func TestNativeBaselineMismatchNeverOpensModelTransport(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		result sandbox.TestResult
		err    error
	}{
		{name: "already healthy", result: sandbox.TestResult{RecipeID: "go-test-workload", ExitCode: 0, Output: "ok"}},
		{name: "different bug", result: sandbox.TestResult{RecipeID: "go-test-workload", ExitCode: 1, Output: "unrelated compilation failure"}},
		{name: "different recipe", result: sandbox.TestResult{RecipeID: "other", ExitCode: 1, Output: "TestRepairFixtureAcceptsSchemaTwo: repair fixture: unsupported job schema version 2"}},
		{name: "timeout", result: sandbox.TestResult{RecipeID: "go-test-workload", ExitCode: 1, TimedOut: true}},
		{name: "truncated", result: sandbox.TestResult{RecipeID: "go-test-workload", ExitCode: 1, Truncated: true}},
		{name: "test unavailable", err: errors.New("offline test runner unavailable")},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			h := newReceiptHarness(t)
			h.claim.Job.Status, h.claim.Job.LeaseUntil = coderepair.JobRunning, time.Now().Add(time.Minute)
			h.claim.Case = coderepair.Case{ID: h.claim.Job.CaseID, BaseSHA: h.base, State: coderepair.StateInvestigating, Version: h.claim.ExpectedVersion}
			h.claim.Attempt = coderepair.Attempt{ID: h.claim.Job.AttemptID, CaseID: h.claim.Job.CaseID}
			tests := &cutoverBaselineTester{result: scenario.result, err: scenario.err}
			gatewayCalls := 0
			engine := &Engine{Process: Process{Version: EngineVersion}, Tests: tests,
				Store: &claimFixture{job: h.claim.Job, caseRecord: h.claim.Case}, Profile: "offline-fixture",
				ConfigVersion: "fixture-v1", Limits: DefaultLimits(),
				Gateway: func(context.Context, agent.ClaimedInvestigation, Fence) (Transport, func(), error) {
					gatewayCalls++
					return Transport{}, nil, errors.New("model transport must not open")
				}}
			result := engine.Investigate(agent.WithClaimedInvestigation(context.Background(), h.claim), h.w,
				h.binding, h.snapshot, coderepair.AgentSelection{Provider: "openai-compatible", Model: "fixture-model", PromptVersion: PromptVersion}, "go-test-workload")
			if result.Status != coderepair.StateBlocked || result.Code != "BASELINE_MISMATCH" || gatewayCalls != 0 || tests.calls != 1 || result.Patch != "" || result.PatchReport.SHA256 != "" {
				t.Fatalf("baseline mismatch opened model transport or produced proof: result=%+v gateway=%d tests=%d", result, gatewayCalls, tests.calls)
			}
		})
	}
}

func TestNativeCannotDetermineLeavesCheckoutUnchanged(t *testing.T) {
	h := newReceiptHarness(t)
	session := h.session(t, false)
	response := session.Execute(context.Background(), ToolRequest{CallID: "cannot-determine", Name: "cannot_determine",
		Args: json.RawMessage(`{"reason":"insufficient source evidence"}`)})
	result := session.Result()
	if response.Status != "ok" || !session.terminal || result.Status != coderepair.StateBlocked || result.Code != "CANNOT_DETERMINE" ||
		result.Patch != "" || result.PatchReport.SHA256 != "" || h.tests.calls != 1 || session.index.Reads != 0 {
		t.Fatalf("no diagnosis produced a hidden patch or test: response=%+v result=%+v", response, result)
	}
	if status := fixtureGit(t, h.w.RootPath(), "status", "--porcelain"); status != "" {
		t.Fatal("blocked diagnosis modified source", status)
	}
}

func TestNativeSingleCandidateFormatterFailureCannotPublish(t *testing.T) {
	s, tests, ctx, good := candidateSession(t, 1)
	bad := strings.Replace(good, "+func rejects(version int) bool { return version != 2 }", "+func rejects(version int) bool { return version != }", 1)
	if bad == good {
		t.Fatal("fixture patch moved")
	}
	s.Execute(ctx, proposal(t, s.workspace.RootPath(), "format-failed", bad, "Schema 2 should be supported"))
	result := s.Result()
	if !s.terminal || result.Status != coderepair.StateFailed || result.Code != "FORMAT_FAILED" || result.Patch != "" || result.PatchReport.SHA256 != "" || tests.calls != 1 {
		t.Fatalf("unformatted source retained proof or ran tests: %+v", result)
	}
	s.Execute(ctx, proposal(t, s.workspace.RootPath(), "after-terminal", good, "Try a valid patch"))
	if tests.calls != 1 || s.candidates != 1 {
		t.Fatal("single-candidate failure silently retried")
	}
}
