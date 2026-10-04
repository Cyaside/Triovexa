package runtimebridge

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/format"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

type matchedWireMetric struct {
	PayloadBytes           int   `json:"payload_bytes"`
	ConservativeInputBound int64 `json:"conservative_input_bound"`
	MessageCount           int   `json:"message_count"`
	MessagesBytes          int   `json:"messages_bytes"`
	ToolDefinitionsBytes   int   `json:"tool_definitions_bytes"`
	ToolDefinitionCount    int   `json:"tool_definition_count"`
}

type matchedEngineMetric struct {
	Calls      []matchedWireMetric `json:"calls"`
	TestCalls  int                 `json:"synthetic_test_calls"`
	ElapsedMS  int64               `json:"wall_time_ms"`
	PatchSHA   string              `json:"patch_sha256"`
	BeforeExit int                 `json:"baseline_exit_code"`
	AfterExit  int                 `json:"candidate_exit_code"`
}

type matchedCorpusCase struct {
	ID            string              `json:"id"`
	SourceBytes   int                 `json:"source_bytes"`
	SourceLines   int                 `json:"source_lines"`
	EvidenceBytes int                 `json:"evidence_bytes"`
	InputSHA256   string              `json:"paired_input_sha256"`
	Legacy        matchedEngineMetric `json:"legacy"`
	Native        matchedEngineMetric `json:"native"`
	PayloadDelta  int                 `json:"native_minus_legacy_payload_bytes"`
	RequestDelta  int                 `json:"native_minus_legacy_requests"`
}

// Wire capture measures the actual final transport body, not a reconstructed
// prompt or fixture-reported token usage. Both sides use the same +512 bound.
func matchedWire(t *testing.T, raw []byte) matchedWireMetric {
	t.Helper()
	var payload struct {
		Messages json.RawMessage   `json:"messages"`
		Tools    []json.RawMessage `json:"tools"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		t.Error("matched corpus received invalid JSON")
		return matchedWireMetric{}
	}
	var messages []json.RawMessage
	if json.Unmarshal(payload.Messages, &messages) != nil {
		t.Error("matched corpus received invalid messages")
	}
	bound, err := admission.ConservativeInputBound(raw, 512)
	if err != nil {
		t.Error(err)
	}
	toolBytes := 0
	if payload.Tools != nil {
		encoded, _ := json.Marshal(payload.Tools)
		toolBytes = len(encoded)
	}
	return matchedWireMetric{len(raw), bound, len(messages), len(payload.Messages), toolBytes, len(payload.Tools)}
}

func matchedSnapshot(t *testing.T, binding coderepair.RepositoryBinding, id string, padding int) coderepair.EvidenceSnapshot {
	t.Helper()
	// Only deterministic diagnostic padding changes within each source cohort.
	// These are 30 workload-size inputs, not 30 independently discovered bugs.
	now := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	log := "valid schema 2 jobs are rejected. " + strings.Repeat("x", padding)
	snapshot, err := coderepair.BuildEvidenceSnapshot(domain.Incident{ID: id, ServiceName: binding.ServiceName, Environment: binding.Environment}, []domain.EvidenceItem{
		{ID: "log-1", IncidentID: id, Type: "log", Source: "fixture-logs", Timestamp: now, Snippet: log},
		{ID: "metric-1", IncidentID: id, Type: "metric", Source: "workload-control", Timestamp: now, Snippet: "backlog is increasing", MetadataJSON: `{"target":"queue-worker","complete":true,"deployed_revision":"` + strings.Repeat("a", 40) + `"}`},
	}, now, coderepair.EvidenceLimits{MaxItems: 4, MaxSnippetBytes: 4096, MaxTotalBytes: 8192, MaxAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func matchedWorkspace(t *testing.T, root string, binding coderepair.RepositoryBinding) *sandbox.Workspace {
	t.Helper()
	w, err := sandbox.Open(root, binding, sandbox.Limits{MaxFileBytes: 32768, MaxTotalBytes: 204800, MaxListedFiles: 20, MaxSearchHits: 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

func matchedResult(t *testing.T, result agent.InvestigationResult, calls []matchedWireMetric, tests *sourceTester, elapsed time.Duration) matchedEngineMetric {
	t.Helper()
	if result.Status != coderepair.StatePatchReady || result.Before.ExitCode != 1 || result.After.ExitCode != 0 || len(calls) != 2 || tests.calls != 2 || result.PatchReport.SHA256 == "" {
		t.Fatalf("matched repair did not follow read/proposal + baseline/candidate sequence: status=%s code=%s requests=%d tests=%d", result.Status, result.Code, len(calls), tests.calls)
	}
	return matchedEngineMetric{calls, tests.calls, elapsed.Milliseconds(), result.PatchReport.SHA256, result.Before.ExitCode, result.After.ExitCode}
}

// TestMatchedRuntimeContextCorpus compares existing runtime implementations
// on the same input and same scripted read/proposal sequence. All network
// traffic terminates at this test's own loopback server. SourceTester is a
// synthetic Go TestRunner; this is neither Docker proof nor live-model quality.
func TestMatchedRuntimeContextCorpus(t *testing.T) {
	entry := os.Getenv("TEST_AGENT_RUNTIME_ENTRY")
	if entry == "" {
		t.Skip("build agent-runtime and set TEST_AGENT_RUNTIME_ENTRY")
	}
	if !filepath.IsAbs(entry) {
		t.Fatal("matched corpus requires an absolute built runtime entry")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	w, binding, _, _, _ := nativeFixture(t)
	root := w.RootPath()
	path := filepath.Join(root, "internal", "workload", "worker.go")
	_ = w.Close()
	cipher, err := secretstore.NewCipher("", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	padding := []int{0, 64, 128, 256, 384, 512, 768, 1024, 1536, 2048}
	cases := make([]matchedCorpusCase, 0, 30)
	for _, lines := range []int{8, 120, 400} {
		original := "package workload\n\nfunc rejects(version int) bool { return version != 1 }\n\n"
		for line := 0; line < lines; line++ {
			original += fmt.Sprintf("// pinned diagnostic source line %03d, schema worker context.\n", line)
		}
		formatted, err := format.Source([]byte(original))
		if err != nil || os.WriteFile(path, formatted, 0600) != nil {
			t.Fatal("matched corpus could not prepare the approved source")
		}
		fixtureGit(t, root, "add", "internal/workload/worker.go")
		fixtureGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "matched source fixture")
		base := fixtureGit(t, root, "rev-parse", "HEAD")
		fixed := strings.Replace(string(formatted), "version != 1", "version != 2", 1)
		if err := os.WriteFile(path, []byte(fixed), 0600); err != nil {
			t.Fatal(err)
		}
		patch := fixtureGit(t, root, "diff", "--", "internal/workload/worker.go") + "\n"
		fixtureGit(t, root, "checkout", "--", "internal/workload/worker.go")
		for index, evidencePadding := range padding {
			id := fmt.Sprintf("matched-%03d-%02d", lines, index)
			t.Run(id, func(t *testing.T) {
				snapshot := matchedSnapshot(t, binding, id, evidencePadding)
				var lock sync.Mutex
				legacyCalls, nativeCalls := []matchedWireMetric{}, []matchedWireMetric{}
				upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					raw, err := io.ReadAll(io.LimitReader(request.Body, MaxFrameBytes+1))
					if err != nil || len(raw) > MaxFrameBytes || request.URL.Path != "/v1/chat/completions" {
						t.Error("matched provider request exceeded the loopback contract")
						writer.WriteHeader(http.StatusBadRequest)
						return
					}
					var fields map[string]json.RawMessage
					if json.Unmarshal(raw, &fields) != nil {
						writer.WriteHeader(http.StatusBadRequest)
						return
					}
					_, native := fields["tools"]
					lock.Lock()
					ordinal := len(legacyCalls) + 1
					if native {
						nativeCalls = append(nativeCalls, matchedWire(t, raw))
						ordinal = len(nativeCalls)
					} else {
						legacyCalls = append(legacyCalls, matchedWire(t, raw))
					}
					lock.Unlock()
					if ordinal > 2 {
						t.Error("matched sequence requested an extra model decision")
						writer.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					var message map[string]any
					if native {
						name := "repo_read"
						args := map[string]any{"path": "internal/workload/worker.go", "start_line": 1, "end_line": 500}
						if ordinal == 2 {
							name, args = "propose_patch", map[string]any{"patch": patch, "hypothesis": "The version check rejects supported schema 2 jobs", "evidence_ids": []string{"log-1", "metric-1"}}
						}
						encoded, _ := json.Marshal(args)
						message = map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": fmt.Sprintf("call-%d", ordinal), "type": "function", "function": map[string]any{"name": name, "arguments": string(encoded)}}}}
					} else {
						decision := agent.Decision{Operation: agent.ReadFile, Path: "internal/workload/worker.go"}
						if ordinal == 2 {
							decision = agent.Decision{Operation: agent.ProposePatch, Patch: patch, Hypothesis: "The version check rejects supported schema 2 jobs", EvidenceIDs: []string{"log-1", "metric-1"}}
						}
						encoded, _ := json.Marshal(decision)
						message = map[string]any{"role": "assistant", "content": string(encoded)}
					}
					writer.Header().Set("Content-Type", "application/json")
					finish := "stop"
					if native {
						finish = "tool_calls"
					}
					_ = json.NewEncoder(writer).Encode(map[string]any{"id": fmt.Sprintf("offline-%d", ordinal), "object": "chat.completion", "created": 1, "model": "fixture-model",
						"choices": []any{map[string]any{"index": 0, "finish_reason": finish, "message": message}}, "usage": map[string]int{"prompt_tokens": 20, "completion_tokens": 10, "total_tokens": 30}})
				}))
				defer upstream.Close()
				provider := ai.ProviderConfig{Name: "openai-compatible", BaseURL: upstream.URL + "/v1", Model: "fixture-model", APIKey: "offline-matched-fixture-key", JSONMode: true, AllowHTTP: true, Timeout: 30 * time.Second}
				client, err := ai.NewOpenAICompatibleClient(provider)
				if err != nil {
					t.Fatal(err)
				}
				legacyTests := &sourceTester{}
				legacy, err := agent.NewLoop(client, legacyTests)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				legacyWorkspace := matchedWorkspace(t, root, binding)
				started := time.Now()
				legacyResult := legacy.Investigate(ctx, legacyWorkspace, binding, snapshot, coderepair.AgentSelection{Provider: provider.Name, Model: provider.Model, PromptVersion: agent.PromptVersion}, "go-test-workload")
				legacyElapsed := time.Since(started)
				fixtureGit(t, root, "checkout", "--", "internal/workload/worker.go")
				nativeWorkspace := matchedWorkspace(t, root, binding)
				store := &claimFixture{job: coderepair.Job{ID: "job-" + id, CaseID: "case-" + id, AttemptID: "attempt-" + id, Status: coderepair.JobRunning, LeaseToken: "fixture-fence", LeaseUntil: time.Now().Add(2 * time.Minute)}, caseRecord: coderepair.Case{ID: "case-" + id, BaseSHA: base, State: coderepair.StateInvestigating, Version: 2}}
				ledger, _ := admission.NewService(admission.NewMemoryStore())
				campaignID := "offline-" + id
				if err := ledger.CreateCampaign(ctx, admission.Campaign{ID: campaignID, Profile: "offline-fixture", Offline: true, MaxRequests: 4, MaxSpendMicroUSD: 200000, MaxInputTokens: 200000}); err != nil {
					t.Fatal(err)
				}
				limits := DefaultLimits()
				limits.MaxInputBytes = 64000 // Offline comparison profile; not final-smoke eligibility.
				nativeTests := &sourceTester{}
				engine := &Engine{Process: Process{Executable: node, Entry: entry, Version: EngineVersion}, Store: store, Tests: nativeTests, Profile: "offline-fixture", ConfigVersion: "matched-v1", Limits: limits}
				engine.Gateway = func(ctx context.Context, claimed agent.ClaimedInvestigation, fence Fence) (Transport, func(), error) {
					gateway, err := modelgateway.New(modelgateway.Config{Provider: provider, Pricing: admission.Pricing{Version: "synthetic-v1", Provider: provider.Name, Model: provider.Model}, CampaignID: campaignID,
						CaseID: claimed.Job.CaseID, AttemptID: claimed.Job.AttemptID, Phase: "repair", ConfigVersion: "matched-v1", MaxInputTokens: 64000, MaxOutputTokens: 1500, MaxRequests: 4, Cipher: cipher, Fence: fence}, ledger)
					if err != nil {
						return Transport{}, nil, err
					}
					url, stop, err := gateway.Listen(ctx)
					return Transport{Model: provider.Model, ModelGatewayURL: url, Capability: gateway.Capability()}, stop, err
				}
				claimed := agent.WithClaimedInvestigation(ctx, agent.ClaimedInvestigation{Job: store.job, Case: store.caseRecord, Attempt: coderepair.Attempt{ID: store.job.AttemptID, CaseID: store.job.CaseID}, ExpectedVersion: 2})
				started = time.Now()
				nativeResult := engine.Investigate(claimed, nativeWorkspace, binding, snapshot, coderepair.AgentSelection{Provider: provider.Name, Model: provider.Model, PromptVersion: PromptVersion}, "go-test-workload")
				nativeElapsed := time.Since(started)
				fixtureGit(t, root, "checkout", "--", "internal/workload/worker.go")
				lock.Lock()
				legacyMetrics := matchedResult(t, legacyResult, append([]matchedWireMetric(nil), legacyCalls...), legacyTests, legacyElapsed)
				nativeMetrics := matchedResult(t, nativeResult, append([]matchedWireMetric(nil), nativeCalls...), nativeTests, nativeElapsed)
				lock.Unlock()
				if legacyMetrics.PatchSHA != nativeMetrics.PatchSHA || legacyResult.Before != nativeResult.Before || legacyResult.After != nativeResult.After {
					t.Fatal("paired runtimes used different patches or test assumptions")
				}
				evidenceJSON, _ := json.Marshal(snapshot)
				input, _ := json.Marshal(map[string]any{"source": string(formatted), "evidence": snapshot, "base": base, "recipe": "go-test-workload", "patch": patch})
				digest := sha256.Sum256(input)
				legacyBytes, nativeBytes := 0, 0
				for _, metric := range legacyMetrics.Calls {
					legacyBytes += metric.PayloadBytes
				}
				for _, metric := range nativeMetrics.Calls {
					nativeBytes += metric.PayloadBytes
				}
				cases = append(cases, matchedCorpusCase{id, len(formatted), strings.Count(string(formatted), "\n"), len(evidenceJSON), hex.EncodeToString(digest[:]), legacyMetrics, nativeMetrics, nativeBytes - legacyBytes, len(nativeMetrics.Calls) - len(legacyMetrics.Calls)})
			})
		}
	}
	if len(cases) != 30 || t.Failed() {
		t.Fatal("all 30 matched corpus inputs must complete before publishing comparison")
	}
	if output := os.Getenv("AI_MATCHED_CORPUS_REPORT"); output != "" {
		if !filepath.IsAbs(output) {
			t.Fatal("matched report requires an absolute local artifact path")
		}
		report := map[string]any{"schema_version": 1, "corpus": "matched-repair-context-v1", "cases": cases, "paired_cases": 30,
			"decision_sequence": []string{"read_full_approved_source", "propose_identical_patch"}, "test_runner": "existing sourceTester: synthetic source-pattern baseline/candidate checks; not Docker or go test",
			"native_engine_version": EngineVersion, "native_prompt_version": PromptVersion, "legacy_prompt_version": agent.PromptVersion, "provider": "scripted-own-loopback", "external_provider_requests": 0,
			"token_measurement": "not_assessed; UTF-8 final payload bytes and the same +512 conservative admission bound are reported separately", "live_model_quality": "not_assessed", "actual_cost": "not_assessed",
			"limitations": []string{"Thirty deterministic workload-size inputs for one known fixture bug, not independent repair-quality cases", "Legacy sends JSON decisions/full source; native sends original seven native schemas/provenance and bounded artifact references", "Native cold process/framework/IPC overhead is included in wall_time_ms; no provider-latency claim", "Offline comparison uses 64000 payload units; it does not grant final-smoke budget or show live pricing", "Scripted proposals do not prove a model read an offloaded artifact or grounded its diagnosis"}}
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil || os.MkdirAll(filepath.Dir(output), 0700) != nil || os.WriteFile(output, encoded, 0600) != nil {
			t.Fatal("matched comparison report could not be saved")
		}
	}
}
