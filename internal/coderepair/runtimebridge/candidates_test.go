package runtimebridge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
)

func candidateSession(t *testing.T, maximum int) (*ToolSession, *sourceTester, context.Context, string) {
	t.Helper()
	w, binding, evidence, base, patch := nativeFixture(t)
	if maximum > 1 {
		// This test-created repository is private. Mirror the ownership marker
		// written by the real sandbox checkout factory, never the working tree.
		fixtureGit(t, w.RootPath(), "checkout", "--detach", base)
		if err := os.WriteFile(filepath.Join(w.RootPath(), ".git", "triovexa-private-checkout"), []byte(w.RootPath()+"\n"+base+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	limits := DefaultLimits()
	limits.MaxCandidateCount = maximum
	tests := &sourceTester{}
	session, err := NewToolSession(w, binding, evidence, coderepair.AgentSelection{Provider: "openai-compatible", Model: "fixture", PromptVersion: PromptVersion}, "go-test-workload", tests, func(context.Context) error { return nil }, limits, base)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	if _, err := session.Baseline(ctx); err != nil {
		t.Fatal(err)
	}
	response := session.Execute(ctx, ToolRequest{CallID: "read-base", Name: "repo_read", Args: json.RawMessage(`{"path":"internal/workload/worker.go","start_line":1,"end_line":4}`)})
	if response.Status != "ok" {
		t.Fatal(response)
	}
	return session, tests, ctx, patch
}

func proposal(t *testing.T, id, patch, explanation string) ToolRequest {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{"patch": patch, "hypothesis": explanation, "evidence_ids": []string{"log-1", "metric-1"}})
	if err != nil {
		t.Fatal(err)
	}
	return ToolRequest{CallID: id, Name: "propose_patch", Args: encoded}
}

func TestSecondCandidateRestoresCleanApprovedBase(t *testing.T) {
	s, tests, ctx, good := candidateSession(t, 2)
	baseline := s.Result().Before
	sourceBytes := s.index.BytesRead
	index := s.index
	bad := strings.Replace(good, "version != 2", "version != 3", 1)
	response := s.Execute(ctx, proposal(t, "patch-bad", bad, "The schema comparison should accept schema 3"))
	feedback, ok := response.Value.(map[string]any)
	if !ok || response.Status != "ok" || feedback["terminal"] != false || feedback["outcome"] != "candidate_feedback" || feedback["code"] != "TESTS_FAILED" || feedback["base_restored"] != true || feedback["candidate_count"] != 1 || feedback["max_candidates"] != 2 {
		t.Fatalf("unexpected candidate feedback: %+v", response)
	}
	if s.terminal || tests.calls != 2 || s.index == index || s.index.Reads != 0 || s.index.BytesRead != sourceBytes || !reflect.DeepEqual(s.Result().Before, baseline) || s.Result().PatchReport.SHA256 != "" {
		t.Fatal("failed candidate reran baseline, retained cache/proof, or became terminal")
	}
	if status := fixtureGit(t, s.workspace.RootPath(), "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatal("candidate did not restore clean base", status)
	}
	read := s.Execute(ctx, ToolRequest{CallID: "read-restored", Name: "repo_read", Args: json.RawMessage(`{"path":"internal/workload/worker.go","start_line":1,"end_line":5}`)})
	source, ok := read.Value.(sandbox.SourceRange)
	if !ok || !strings.Contains(source.Text, "version != 1") || s.index.Reads != 1 || s.index.BytesRead != sourceBytes {
		t.Fatal("restored source did not invalidate candidate cache")
	}
	final := s.Execute(ctx, proposal(t, "patch-good", good, "Schema 2 jobs are rejected by the version check"))
	value, ok := final.Value.(map[string]any)
	if !ok || value["terminal"] != true || s.Result().Status != coderepair.StatePatchReady || s.Result().Code != "PATCH_VERIFIED" || tests.calls != 3 || s.candidates != 2 || !reflect.DeepEqual(s.Result().Before, baseline) {
		t.Fatalf("corrected candidate did not preserve authoritative red-green proof: %+v", final)
	}
	if _, err := s.Result().Outcome(coderepair.Job{ID: "job", CaseID: "case", AttemptID: "attempt", LeaseToken: "fence"}, 1, "go-test-workload", time.Now()); err != nil {
		t.Fatal("corrected result cannot produce publication proof", err)
	}
}

func TestSingleCandidateFailureHasNoHiddenCall(t *testing.T) {
	s, tests, ctx, good := candidateSession(t, 1)
	response := s.Execute(ctx, proposal(t, "patch-bad", strings.Replace(good, "version != 2", "version != 3", 1), "Try schema 3"))
	value, ok := response.Value.(map[string]any)
	if !ok || value["terminal"] != true || s.Result().Code != "TESTS_FAILED" || tests.calls != 2 || s.Result().PatchReport.SHA256 != "" {
		t.Fatalf("single candidate silently corrected or retained failed proof: %+v", response)
	}
	s.Execute(ctx, proposal(t, "patch-after-terminal", good, "Try schema 2"))
	if tests.calls != 2 || s.candidates != 1 {
		t.Fatal("terminal candidate failure triggered another test or proposal")
	}
}

func TestCandidateFormatterFeedbackRestoresBaseWithoutRunningFailedSource(t *testing.T) {
	s, tests, ctx, good := candidateSession(t, 2)
	bad := strings.Replace(good, "+func rejects(version int) bool { return version != 2 }", "+func rejects(version int) bool { return version != }", 1)
	if bad == good {
		t.Fatal("fixture patch moved")
	}
	response := s.Execute(ctx, proposal(t, "patch-invalid-source", bad, "The supported schema is rejected"))
	value, ok := response.Value.(map[string]any)
	if !ok || value["terminal"] != false || value["code"] != "FORMAT_FAILED" || value["base_restored"] != true || tests.calls != 1 {
		t.Fatalf("formatter failure ran invalid source or became hidden retry: %+v", response)
	}
	s.Execute(ctx, proposal(t, "patch-correct-source", good, "Schema 2 must be accepted"))
	if s.Result().Status != coderepair.StatePatchReady || tests.calls != 2 {
		t.Fatal("explicit corrected source did not run the single allowed post-patch test")
	}
}

func TestCorrectionRejectsRepeatedPatchWithChangedExplanation(t *testing.T) {
	s, tests, ctx, good := candidateSession(t, 3)
	bad := strings.Replace(good, "version != 2", "version != 3", 1)
	s.Execute(ctx, proposal(t, "patch-first", bad, "Try schema 3"))
	response := s.Execute(ctx, proposal(t, "patch-repeat", bad+"\n", "Different explanation does not change the patch"))
	value, ok := response.Value.(map[string]any)
	if !ok || value["terminal"] != true || s.Result().Code != "NO_PROGRESS" || tests.calls != 2 || s.candidates != 1 {
		t.Fatalf("repeated patch dispatched another candidate: %+v", response)
	}
}

type dirtyCandidateTester struct{ delegate *sourceTester }

func (d dirtyCandidateTester) Run(ctx context.Context, root string, binding coderepair.RepositoryBinding, recipe string) (sandbox.TestResult, error) {
	result, err := d.delegate.Run(ctx, root, binding, recipe)
	if d.delegate.calls == 2 {
		if writeErr := os.WriteFile(filepath.Join(root, "internal", "workload", "unexpected.go"), []byte("package workload\n"), 0600); writeErr != nil {
			return result, writeErr
		}
	}
	return result, err
}

func TestCandidateCorrectionBlocksUnexpectedWorktreeChanges(t *testing.T) {
	s, tests, ctx, good := candidateSession(t, 2)
	s.tests = dirtyCandidateTester{tests}
	response := s.Execute(ctx, proposal(t, "patch-dirty", strings.Replace(good, "version != 2", "version != 3", 1), "Try schema 3"))
	value, ok := response.Value.(map[string]any)
	if !ok || value["terminal"] != true || s.Result().Code != "CANDIDATE_RESTORE_FAILED" || tests.calls != 2 || s.Result().PatchReport.SHA256 != "" {
		t.Fatalf("unexpected worktree mutation was silently repaired: %+v", response)
	}
	if _, err := os.Stat(filepath.Join(s.workspace.RootPath(), "internal", "workload", "unexpected.go")); err != nil {
		t.Fatal("restore removed an unowned new file")
	}
}
