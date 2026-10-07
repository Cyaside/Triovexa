package runtimebridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/ai/admission"
	"github.com/Cyaside/Triovexa/internal/ai/modelgateway"
	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/agent"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/secretstore"
)

type claimFixture struct {
	job        coderepair.Job
	caseRecord coderepair.Case
}

func (s *claimFixture) GetRepairJob(context.Context, string) (coderepair.Job, error) {
	return s.job, nil
}
func (s *claimFixture) GetRepairCase(context.Context, string) (coderepair.Case, error) {
	return s.caseRecord, nil
}

type sourceTester struct{ calls int }

type countedTester struct {
	delegate agent.TestRunner
	calls    int
}

func (s *countedTester) Run(ctx context.Context, root string, binding coderepair.RepositoryBinding, recipe string) (sandbox.TestResult, error) {
	s.calls++
	return s.delegate.Run(ctx, root, binding, recipe)
}

func (s *sourceTester) Run(_ context.Context, root string, _ coderepair.RepositoryBinding, recipe string) (sandbox.TestResult, error) {
	s.calls++
	source, err := os.ReadFile(filepath.Join(root, "internal", "workload", "worker.go"))
	if err != nil {
		return sandbox.TestResult{}, err
	}
	if strings.Contains(string(source), "version != 2") {
		return sandbox.TestResult{RecipeID: recipe, ExitCode: 0, Output: "ok"}, nil
	}
	return sandbox.TestResult{RecipeID: recipe, ExitCode: 1, Output: "TestRepairFixtureAcceptsSchemaTwo: repair fixture: unsupported job schema version 2"}, nil
}

func fixtureGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git: %v %s", err, output)
	}
	return strings.TrimSpace(string(output))
}

func nativeFixture(t *testing.T) (*sandbox.Workspace, coderepair.RepositoryBinding, coderepair.EvidenceSnapshot, string, string) {
	t.Helper()
	root := t.TempDir()
	// Docker's non-root user reads this mount; the private parent stays 0700.
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, root, "init", "-b", "main")
	fixtureGit(t, root, "config", "core.autocrlf", "false")
	path := filepath.Join(root, "internal", "workload", "worker.go")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	original := "package workload\n\nfunc rejects(version int) bool { return version != 1 }\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n\ngo 1.24.0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	regression := "//go:build repair_regression\n\npackage workload\n\nimport \"testing\"\n\nfunc TestRepairFixtureAcceptsSchemaTwo(t *testing.T) {\n\tif rejects(2) { t.Fatal(\"repair fixture: unsupported job schema version 2\") }\n}\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "worker_regression_test.go"), []byte(regression), 0644); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, root, "add", "go.mod", "internal/workload")
	fixtureGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "fixture")
	base := fixtureGit(t, root, "rev-parse", "HEAD")
	if err := os.WriteFile(path, []byte(strings.Replace(original, "version != 1", "version != 2", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	patch := fixtureGit(t, root, "diff", "--", "internal/workload/worker.go") + "\n"
	fixtureGit(t, root, "checkout", "--", "internal/workload/worker.go")
	binding := coderepair.RepositoryBinding{ID: "binding", ServiceName: "queue-worker", Environment: "staging",
		RepositoryURL: "https://github.com/Cyaside/Triovexa", BaseRef: "main", AllowedPaths: []string{"internal/workload"},
		TestRecipes: []string{"go-test-workload"}, PolicyVersion: "repair-v1", Enabled: true}
	workspace, err := sandbox.Open(root, binding, sandbox.Limits{MaxFileBytes: 32768, MaxTotalBytes: 204800, MaxListedFiles: 20, MaxSearchHits: 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	now := time.Now().UTC()
	snapshot, err := coderepair.BuildEvidenceSnapshot(domain.Incident{ID: "incident", ServiceName: binding.ServiceName, Environment: binding.Environment}, []domain.EvidenceItem{
		{ID: "log-1", IncidentID: "incident", Type: "log", Source: "fixture-logs", Timestamp: now, Snippet: "valid schema 2 jobs are rejected"},
		{ID: "metric-1", IncidentID: "incident", Type: "metric", Source: "workload-control", Timestamp: now, Snippet: "backlog is increasing", MetadataJSON: `{"target":"queue-worker","complete":true,"deployed_revision":"` + strings.Repeat("a", 40) + `"}`},
	}, now, coderepair.EvidenceLimits{MaxItems: 4, MaxSnippetBytes: 1024, MaxTotalBytes: 2048, MaxAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return workspace, binding, snapshot, base, patch
}

func TestToolDispatchRejectsLostLease(t *testing.T) {
	w, binding, snapshot, base, _ := nativeFixture(t)
	var valid atomic.Bool
	valid.Store(true)
	tests := &sourceTester{}
	fence := func(context.Context) error {
		if !valid.Load() {
			return fmt.Errorf("lease lost")
		}
		return nil
	}
	tools, err := NewToolSession(w, binding, snapshot, coderepair.AgentSelection{Provider: "openai-compatible", Model: "fixture", PromptVersion: PromptVersion}, "go-test-workload", tests, fence, DefaultLimits(), base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tools.Baseline(context.Background()); err != nil {
		t.Fatal(err)
	}
	valid.Store(false)
	result := tools.Execute(context.Background(), ToolRequest{CallID: "read", Name: "repo_read", Args: json.RawMessage(`{"path":"internal/workload/worker.go","start_line":1,"end_line":4}`)})
	if result.Status != "ok" || tools.Result().Code != "LEASE_LOST" || tools.index.Reads != 0 || tests.calls != 1 {
		t.Fatal("lost lease performed a new tool side effect")
	}
}

func TestRuntimeCannotSelectRepositoryOrProvider(t *testing.T) {
	w, binding, snapshot, base, _ := nativeFixture(t)
	tools, err := NewToolSession(w, binding, snapshot, coderepair.AgentSelection{Provider: "openai-compatible", Model: "fixture", PromptVersion: PromptVersion}, "go-test-workload", &sourceTester{}, func(context.Context) error { return nil }, DefaultLimits(), base)
	if err != nil {
		t.Fatal(err)
	}
	result := tools.Execute(context.Background(), ToolRequest{CallID: "read", Name: "repo_read", Args: json.RawMessage(`{"path":"internal/workload/worker.go","start_line":1,"end_line":4,"repository":"https://other.example/repo","base_url":"https://other.example"}`)})
	if result.Status != "error" || tools.index.Reads != 0 {
		t.Fatal("tool selected new authority")
	}
	binding.ServiceName = "different-service"
	if _, err := NewToolSession(w, binding, snapshot, coderepair.AgentSelection{}, "go-test-workload", &sourceTester{}, func(context.Context) error { return nil }, DefaultLimits(), base); err == nil {
		t.Fatal("constructor accepted evidence from another service")
	}
}

func TestNoProgressStopsRepeatedToolOrDiff(t *testing.T) {
	w, binding, snapshot, base, _ := nativeFixture(t)
	tools, err := NewToolSession(w, binding, snapshot, coderepair.AgentSelection{Provider: "openai-compatible", Model: "fixture", PromptVersion: PromptVersion}, "go-test-workload", &sourceTester{}, func(context.Context) error { return nil }, DefaultLimits(), base)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		tools.Execute(context.Background(), ToolRequest{CallID: fmt.Sprintf("read-%d", i), Name: "repo_read", Args: json.RawMessage(`{"path":"internal/workload/worker.go","start_line":1,"end_line":4}`)})
	}
	if tools.Result().Code != "NO_PROGRESS" || tools.index.Reads != 1 {
		t.Fatal("repeat query did not stop with one source read")
	}
}

func TestNativeRuntimeIPCProducesGoVerifiedPatchIntegration(t *testing.T) {
	entry := os.Getenv("TEST_AGENT_RUNTIME_ENTRY")
	if entry == "" {
		t.Skip("build agent-runtime and set TEST_AGENT_RUNTIME_ENTRY")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	w, binding, snapshot, base, patch := nativeFixture(t)
	store := &claimFixture{job: coderepair.Job{ID: "job", CaseID: "case", AttemptID: "attempt", Status: coderepair.JobRunning, LeaseToken: "fence", LeaseUntil: time.Now().Add(5 * time.Minute)}, caseRecord: coderepair.Case{ID: "case", BaseSHA: base, State: coderepair.StateInvestigating, Version: 2}}
	var calls atomic.Int32
	var payloadBytes []int
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		index := calls.Add(1)
		var body map[string]json.RawMessage
		raw, readErr := io.ReadAll(io.LimitReader(request.Body, MaxFrameBytes))
		payloadBytes = append(payloadBytes, len(raw))
		if readErr != nil || json.Unmarshal(raw, &body) != nil {
			t.Error("invalid wire body")
		}
		name := "repo_read"
		args := any(map[string]any{"path": "internal/workload/worker.go", "start_line": 1, "end_line": 20})
		if index == 2 {
			name = "propose_patch"
			args = map[string]any{"patch": patch, "hypothesis": "The version check rejects supported schema 2 jobs", "evidence_ids": []string{"log-1", "metric-1"}}
		}
		encoded, _ := json.Marshal(args)
		response := map[string]any{"id": fmt.Sprintf("completion-%d", index), "model": "fixture-model", "object": "chat.completion", "created": 1,
			"choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": fmt.Sprintf("call-%d", index), "type": "function", "function": map[string]any{"name": name, "arguments": string(encoded)}}}}}},
			"usage":   map[string]int{"prompt_tokens": 20, "completion_tokens": 10, "total_tokens": 30}}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(response)
	}))
	defer upstream.Close()
	ledger, _ := admission.NewService(admission.NewMemoryStore())
	if err := ledger.CreateCampaign(context.Background(), admission.Campaign{ID: "native-offline", Profile: "offline-fixture", Offline: true, MaxRequests: 6, MaxSpendMicroUSD: 200000, MaxInputTokens: 200000}); err != nil {
		t.Fatal(err)
	}
	cipher, err := secretstore.NewCipher("", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	limits := DefaultLimits()
	limits.MaxInputBytes = 64000
	var testAdapter agent.TestRunner = &sourceTester{}
	if image := os.Getenv("TEST_REPAIR_SANDBOX_IMAGE"); image != "" {
		testAdapter = sandbox.DockerTester{Image: image}
	}
	tests := &countedTester{delegate: testAdapter}
	engine := &Engine{Process: Process{Executable: node, Entry: entry, Version: EngineVersion}, Store: store, Tests: tests, Profile: "offline-fixture", ConfigVersion: "fixture-v1", Limits: limits}
	engine.Gateway = func(ctx context.Context, claim agent.ClaimedInvestigation, fence Fence) (Transport, func(), error) {
		provider := ai.ProviderConfig{Name: "openai-compatible", BaseURL: upstream.URL, APIKey: "fixture-key", Model: "fixture-model", AllowHTTP: true}
		gateway, err := modelgateway.New(modelgateway.Config{Provider: provider, Pricing: admission.Pricing{Version: "synthetic-v1", Provider: provider.Name, Model: provider.Model}, CampaignID: "native-offline", CaseID: claim.Job.CaseID, AttemptID: claim.Job.AttemptID, Phase: "repair", ConfigVersion: "fixture-v1", MaxInputTokens: 64000, MaxOutputTokens: 1500, MaxRequests: 4, Cipher: cipher, Fence: fence}, ledger)
		if err != nil {
			return Transport{}, nil, err
		}
		url, stop, err := gateway.Listen(ctx)
		return Transport{Model: provider.Model, ModelGatewayURL: url, Capability: gateway.Capability()}, stop, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	ctx = agent.WithClaimedInvestigation(ctx, agent.ClaimedInvestigation{Job: store.job, Case: store.caseRecord, Attempt: coderepair.Attempt{ID: store.job.AttemptID, CaseID: store.job.CaseID}, ExpectedVersion: 2})
	started := time.Now()
	result := engine.Investigate(ctx, w, binding, snapshot, coderepair.AgentSelection{Provider: "openai-compatible", Model: "fixture-model", PromptVersion: PromptVersion}, "go-test-workload")
	if result.Status != coderepair.StatePatchReady || result.Before.ExitCode != 1 || result.After.ExitCode != 0 || result.PatchReport.SHA256 == "" || calls.Load() != 2 || tests.calls != 2 {
		t.Fatalf("IPC repair: status=%s code=%s reason=%s model=%d tests=%d", result.Status, result.Code, result.Reason, calls.Load(), tests.calls)
	}
	if _, err := result.Outcome(store.job, 2, "go-test-workload", time.Now().UTC()); err != nil {
		t.Fatal("native result cannot produce existing publication proof", err)
	}
	if reportPath := os.Getenv("AI_NATIVE_REPORT"); reportPath != "" {
		totalBytes := 0
		for _, size := range payloadBytes {
			totalBytes += size
		}
		report := map[string]any{"schema_version": 1, "engine": "deepagents", "engine_version": EngineVersion, "profile": "offline-fixture",
			"model_requests": calls.Load(), "payload_bytes_per_request": payloadBytes, "total_payload_bytes": totalBytes,
			"conservative_input_bound": totalBytes + 512*len(payloadBytes), "wall_time_ms": time.Since(started).Milliseconds(), "test_calls": tests.calls,
			"external_provider_requests": 0, "live_model_quality": "not_assessed", "docker_tests": os.Getenv("TEST_REPAIR_SANDBOX_IMAGE") != ""}
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(reportPath), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(reportPath, encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
