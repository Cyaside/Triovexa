package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
	"github.com/Cyaside/Triovexa/internal/domain"
)

type scriptedCompleter struct {
	decisions []string
	err       error
	calls     int
}

func (s *scriptedCompleter) CompleteJSONDetailed(_ context.Context, messages []ai.ChatMessage) (ai.CompletionResult, error) {
	if len(messages) < 2 || !strings.Contains(messages[1].Content, "UNTRUSTED INCIDENT EVIDENCE") {
		return ai.CompletionResult{}, errors.New("evidence not supplied")
	}
	if s.err != nil {
		return ai.CompletionResult{}, s.err
	}
	if s.calls >= len(s.decisions) {
		return ai.CompletionResult{}, errors.New("script exhausted")
	}
	content := s.decisions[s.calls]
	s.calls++
	return ai.CompletionResult{Content: content, Provider: "openai-compatible", Model: "fixture-model",
		Usage: ai.CompletionUsage{PromptTokens: 10, CompletionTokens: 4, TotalTokens: 14}}, nil
}

type sourceAwareTester struct {
	forceFailure bool
	calls        int
}

func (s *sourceAwareTester) Run(_ context.Context, root string, _ coderepair.RepositoryBinding, recipeID string) (sandbox.TestResult, error) {
	s.calls++
	if recipeID != "go-test-workload" {
		return sandbox.TestResult{}, errors.New("unexpected recipe")
	}
	data, err := os.ReadFile(filepath.Join(root, "internal", "workload", "repair_fixture.go"))
	if err != nil {
		return sandbox.TestResult{}, err
	}
	if strings.Contains(string(data), "version != 2") && !s.forceFailure {
		return sandbox.TestResult{RecipeID: recipeID, ExitCode: 0, Output: "ok"}, nil
	}
	return sandbox.TestResult{RecipeID: recipeID, ExitCode: 1,
		Output: "TestRepairFixtureAcceptsSchemaTwo: repair fixture: unsupported job schema version 2"}, nil
}

func runLoopGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func loopFixture(t *testing.T) (*sandbox.Workspace, coderepair.RepositoryBinding, coderepair.EvidenceSnapshot, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git executable unavailable")
	}
	root := t.TempDir()
	runLoopGit(t, root, "init", "-b", "main")
	runLoopGit(t, root, "config", "core.autocrlf", "false")
	path := filepath.Join(root, "internal", "workload", "repair_fixture.go")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package workload\n\nfunc accepts(version int) bool { return version != 1 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "workload", "repair_regression_test.go"),
		[]byte("package workload\n// regression test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runLoopGit(t, root, "add", "internal/workload")
	runLoopGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid",
		"-c", "commit.gpgsign=false", "commit", "-m", "fixture")
	binding := coderepair.RepositoryBinding{ID: "binding-1", ServiceName: "queue-worker", Environment: "staging",
		RepositoryURL: "https://github.com/Cyaside/Triovexa", BaseRef: "main",
		AllowedPaths: []string{"internal/workload"}, TestRecipes: []string{"go-test-workload"},
		PolicyVersion: "repair-v1", Enabled: true}
	workspace, err := sandbox.Open(root, binding, sandbox.Limits{
		MaxFileBytes: 32 * 1024, MaxTotalBytes: 200 * 1024, MaxListedFiles: 20, MaxSearchHits: 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	now := time.Now().UTC()
	incident := domain.Incident{ID: "incident-1", ServiceName: binding.ServiceName, Environment: binding.Environment}
	snapshot, err := coderepair.BuildEvidenceSnapshot(incident, []domain.EvidenceItem{
		{ID: "log-1", IncidentID: incident.ID, Type: "log", Source: "grafana-loki", Timestamp: now,
			Snippet: "valid schema 2 jobs repeatedly fail with unsupported schema"},
		{ID: "metric-1", IncidentID: incident.ID, Type: "metric", Source: "workload-control", Timestamp: now,
			Snippet: "backlog rising", MetadataJSON: `{"target":"queue-worker","complete":true,"deployed_revision":"` + strings.Repeat("a", 40) + `"}`},
	}, now, coderepair.EvidenceLimits{MaxItems: 4, MaxSnippetBytes: 1024, MaxTotalBytes: 2048, MaxAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return workspace, binding, snapshot, root
}

func fixturePatch(t *testing.T, root, name string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(content), "version != 1", "version != 2", 1)
	if name == "internal/workload/repair_regression_test.go" {
		updated = strings.Replace(string(content), "regression test", "removed regression", 1)
	}
	if err := os.WriteFile(path, []byte(updated), 0600); err != nil {
		t.Fatal(err)
	}
	patch := runLoopGit(t, root, "diff", "--", name)
	runLoopGit(t, root, "checkout", "--", name)
	return patch
}

func TestInvestigationProducesGroundedRedToGreenPatch(t *testing.T) {
	w, binding, snapshot, root := loopFixture(t)
	patch := fixturePatch(t, root, "internal/workload/repair_fixture.go")
	model := &scriptedCompleter{decisions: []string{
		`{"operation":"read_file","path":"internal/workload/repair_fixture.go"}`,
		`{"operation":"propose_patch","hypothesis":"Schema 2 jobs fail because the version check rejects them","evidence_ids":["log-1","metric-1"],"patch":` + jsonString(t, patch) + `}`,
	}}
	tests := &sourceAwareTester{}
	loop, err := NewLoop(model, tests)
	if err != nil {
		t.Fatal(err)
	}
	result := loop.Investigate(context.Background(), w, binding, snapshot, coderepair.AgentSelection{
		Provider: "openai-compatible", Model: "fixture-model", PromptVersion: PromptVersion}, "go-test-workload")
	if result.Status != coderepair.StatePatchReady || result.Code != "PATCH_VERIFIED" || result.Before.ExitCode != 1 ||
		result.After.ExitCode != 0 || tests.calls != 2 || len(result.EvidenceIDs) != 2 ||
		result.PatchReport.SHA256 == "" || result.Usage.TotalTokens != 28 {
		t.Fatalf("unexpected investigation result: %+v, test calls=%d", result, tests.calls)
	}
}

func TestInvestigationFailsClosedWithoutFallbackPatch(t *testing.T) {
	for name, scenario := range map[string]struct {
		decisions    []string
		providerErr  error
		forceFailure bool
		wantStatus   coderepair.State
		wantCode     string
	}{
		"provider error": {providerErr: errors.New("provider timed out"), wantStatus: coderepair.StateBlocked, wantCode: "PROVIDER_ERROR"},
		"invalid JSON":   {decisions: []string{`not-json`}, wantStatus: coderepair.StateFailed, wantCode: "INVALID_DECISION"},
		"no diagnosis":   {decisions: []string{`{"operation":"cannot_determine","reason":"insufficient source evidence"}`}, wantStatus: coderepair.StateBlocked, wantCode: "CANNOT_DETERMINE"},
	} {
		t.Run(name, func(t *testing.T) {
			w, binding, snapshot, root := loopFixture(t)
			model := &scriptedCompleter{decisions: scenario.decisions, err: scenario.providerErr}
			loop, err := NewLoop(model, &sourceAwareTester{forceFailure: scenario.forceFailure})
			if err != nil {
				t.Fatal(err)
			}
			result := loop.Investigate(context.Background(), w, binding, snapshot, coderepair.AgentSelection{
				Provider: "openai-compatible", Model: "fixture-model", PromptVersion: PromptVersion}, "go-test-workload")
			if result.Status != scenario.wantStatus || result.Code != scenario.wantCode || result.Patch != "" {
				t.Fatalf("unexpected fail-closed result: %+v", result)
			}
			if changed := runLoopGit(t, root, "status", "--porcelain"); changed != "" {
				t.Fatalf("failure modified checkout: %s", changed)
			}
		})
	}
}

func TestInvestigationCannotEditRegressionTest(t *testing.T) {
	w, binding, snapshot, root := loopFixture(t)
	patch := fixturePatch(t, root, "internal/workload/repair_regression_test.go")
	model := &scriptedCompleter{decisions: []string{
		`{"operation":"read_file","path":"internal/workload/repair_regression_test.go"}`,
		`{"operation":"propose_patch","hypothesis":"remove failing test","evidence_ids":["log-1"],"patch":` + jsonString(t, patch) + `}`,
	}}
	tests := &sourceAwareTester{}
	loop, err := NewLoop(model, tests)
	if err != nil {
		t.Fatal(err)
	}
	result := loop.Investigate(context.Background(), w, binding, snapshot, coderepair.AgentSelection{
		Provider: "openai-compatible", Model: "fixture-model", PromptVersion: PromptVersion}, "go-test-workload")
	if result.Status != coderepair.StateFailed || result.Code != "UNGROUNDED_PATCH" || tests.calls != 1 {
		t.Fatalf("test tampering was not blocked: %+v", result)
	}
	if changed := runLoopGit(t, root, "status", "--porcelain"); changed != "" {
		t.Fatalf("test tampering modified checkout: %s", changed)
	}
}

func TestInvestigationDoesNotPublishPatchWhenPostTestFails(t *testing.T) {
	w, binding, snapshot, root := loopFixture(t)
	patch := fixturePatch(t, root, "internal/workload/repair_fixture.go")
	model := &scriptedCompleter{decisions: []string{
		`{"operation":"read_file","path":"internal/workload/repair_fixture.go"}`,
		`{"operation":"propose_patch","hypothesis":"schema 2 fails at version check","evidence_ids":["log-1"],"patch":` + jsonString(t, patch) + `}`,
	}}
	tests := &sourceAwareTester{forceFailure: true}
	loop, err := NewLoop(model, tests)
	if err != nil {
		t.Fatal(err)
	}
	result := loop.Investigate(context.Background(), w, binding, snapshot, coderepair.AgentSelection{
		Provider: "openai-compatible", Model: "fixture-model", PromptVersion: PromptVersion}, "go-test-workload")
	if result.Status != coderepair.StateFailed || result.Code != "TESTS_FAILED" || result.Patch != "" || tests.calls != 2 {
		t.Fatalf("failed regression produced publishable patch: %+v, calls=%d", result, tests.calls)
	}
}

func TestInvestigationBlocksWhenBaselineDoesNotMatchBug(t *testing.T) {
	w, binding, snapshot, root := loopFixture(t)
	file := filepath.Join(root, "internal", "workload", "repair_fixture.go")
	if err := os.WriteFile(file, []byte("package workload\n\nfunc accepts(version int) bool { return version != 2 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	model := &scriptedCompleter{decisions: []string{`{"operation":"cannot_determine","reason":"unknown"}`}}
	loop, err := NewLoop(model, &sourceAwareTester{})
	if err != nil {
		t.Fatal(err)
	}
	result := loop.Investigate(context.Background(), w, binding, snapshot, coderepair.AgentSelection{
		Provider: "openai-compatible", Model: "fixture-model", PromptVersion: PromptVersion}, "go-test-workload")
	if result.Status != coderepair.StateBlocked || result.Code != "BASELINE_MISMATCH" || model.calls != 0 {
		t.Fatalf("baseline mismatch reached model: %+v calls=%d", result, model.calls)
	}
}

func TestInvestigationRejectsUnformattedPatch(t *testing.T) {
	w, binding, snapshot, root := loopFixture(t)
	file := filepath.Join(root, "internal", "workload", "repair_fixture.go")
	if err := os.WriteFile(file, []byte("package workload\n\nfunc accepts(version int) bool {return version != 2}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	patch := runLoopGit(t, root, "diff", "--", "internal/workload/repair_fixture.go")
	runLoopGit(t, root, "checkout", "--", "internal/workload/repair_fixture.go")
	model := &scriptedCompleter{decisions: []string{
		`{"operation":"read_file","path":"internal/workload/repair_fixture.go"}`,
		`{"operation":"propose_patch","hypothesis":"schema 2 fails at version check","evidence_ids":["log-1"],"patch":` + jsonString(t, patch) + `}`,
	}}
	loop, err := NewLoop(model, &sourceAwareTester{})
	if err != nil {
		t.Fatal(err)
	}
	result := loop.Investigate(context.Background(), w, binding, snapshot, coderepair.AgentSelection{
		Provider: "openai-compatible", Model: "fixture-model", PromptVersion: PromptVersion}, "go-test-workload")
	if result.Status != coderepair.StateFailed || result.Code != "FORMAT_FAILED" || result.Patch != "" {
		t.Fatalf("unformatted patch became publishable: %+v", result)
	}
}

func jsonString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
