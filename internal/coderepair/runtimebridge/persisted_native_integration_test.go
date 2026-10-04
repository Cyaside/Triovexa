package runtimebridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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
	"github.com/Cyaside/Triovexa/internal/secretstore"
	"github.com/Cyaside/Triovexa/internal/storage/postgres"
	"github.com/google/uuid"
)

type persistedDockerTester struct {
	image string
	calls int
	last  sandbox.TestResult
	err   error
}

func (tester *persistedDockerTester) Run(ctx context.Context, root string, binding coderepair.RepositoryBinding, recipe string) (sandbox.TestResult, error) {
	tester.calls++
	tester.last, tester.err = (sandbox.DockerTester{Image: tester.image}).Run(ctx, root, binding, recipe)
	return tester.last, tester.err
}

func TestPersistedNativeRepairRecoversTerminalProofIntegration(t *testing.T) {
	f := persistedNativeFixture(t)
	w, binding, initialSnapshot, base, patch := nativeFixture(t)
	var calls atomic.Int32
	var payloadBytes atomic.Int64
	var requestBytes [2]atomic.Int64
	var snapshot coderepair.EvidenceSnapshot
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		ordinal := calls.Add(1)
		var body json.RawMessage
		if json.NewDecoder(request.Body).Decode(&body) != nil {
			t.Error("invalid native provider wire body")
		}
		payloadBytes.Add(int64(len(body)))
		if ordinal <= 2 {
			requestBytes[ordinal-1].Store(int64(len(body)))
		}
		if request.Header.Get("Authorization") != "Bearer synthetic-native-model-key" || strings.Contains(string(body), "synthetic-native-checkpoint-password") {
			t.Error("checkpoint secret crossed model boundary or credential header changed")
		}
		if ordinal > 2 {
			t.Error("completed repair requested an unplanned model call")
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		name := "repo_read"
		args := any(map[string]any{"path": "internal/workload/worker.go", "start_line": 1, "end_line": 20})
		if ordinal == 2 {
			name = "propose_patch"
			ids := make([]string, 0, len(snapshot.Entries))
			for _, evidence := range snapshot.Entries {
				ids = append(ids, evidence.ID)
			}
			args = map[string]any{"patch": patch, "hypothesis": "version check rejects supported schema two jobs", "evidence_ids": ids}
		}
		encoded, _ := json.Marshal(args)
		response := map[string]any{"id": fmt.Sprintf("fixture-%d", ordinal), "model": "fixture-model", "object": "chat.completion", "created": 1,
			"choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "content": nil,
				"reasoning_content": "synthetic native reasoning", "tool_calls": []any{map[string]any{"id": fmt.Sprintf("call-%d", ordinal), "type": "function",
					"function": map[string]any{"name": name, "arguments": string(encoded)}}}}}},
			"usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 10, "total_tokens": 30,
				"prompt_tokens_details": map[string]int{"cached_tokens": 5}, "completion_tokens_details": map[string]int{"reasoning_tokens": 3}}}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(response)
	}))
	t.Cleanup(upstream.Close)
	provider := ai.ProviderConfig{Name: "openai-compatible", BaseURL: upstream.URL, Model: "fixture-model", APIKey: "synthetic-native-model-key", AllowHTTP: true, Timeout: 5 * time.Second}
	client, err := ai.NewOpenAICompatibleClient(provider)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secretstore.NewCipher("", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	// Match final-smoke numerical limits while keeping all inference offline.
	// A successful synthetic run makes no claim about live model quality.
	budget := modelgateway.BudgetConfig{Campaign: admission.Campaign{ID: "native-" + uuid.NewString(), Profile: "offline-fixture", Offline: true,
		MaxRequests: 6, MaxSpendMicroUSD: 200000, MaxInputTokens: 12000}, Pricing: admission.Pricing{Version: "synthetic-v1", Provider: provider.Name, Model: provider.Model},
		ConfigVersion: "fixture-v1", MaxInputTokens: 6000, MaxOutputTokens: 1500}
	selection, err := SealSelection(client, budget, cipher)
	if err != nil {
		t.Fatal(err)
	}
	job, repairCase, attempt, snapshot := approveNativeFixture(t, f.store, binding, base, initialSnapshot.DeployedRevision, selection)
	ledger, _ := admission.NewService(f.store.ModelBudgetStore())
	tests := &persistedDockerTester{image: f.image}
	runner := &NativeRunner{Process: Process{Executable: f.node, Entry: f.entry, Version: EngineVersion}, Tests: tests, Store: f.store,
		Ledger: ledger, Cipher: cipher, CheckpointDSN: f.checkpointDSN, CheckpointSchema: f.checkpoint}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	claimed := agent.ClaimedInvestigation{Job: job, Case: repairCase, Attempt: attempt, ExpectedVersion: repairCase.Version}
	result := runner.Investigate(agent.WithClaimedInvestigation(ctx, claimed), w, binding, snapshot, selection, "go-test-workload")
	if result.Status != coderepair.StatePatchReady || result.Before.ExitCode != 1 || result.After.ExitCode != 0 || calls.Load() != 2 || tests.calls != 2 || result.Runtime == nil || result.Runtime.Accounting.ModelRequests != 2 || result.Runtime.Accounting.UsageStatus != "known" {
		t.Fatalf("persisted native chain: status=%s code=%s reason=%s models=%d tests=%d test_result=%+v test_error=%v runtime=%+v", result.Status, result.Code, result.Reason, calls.Load(), tests.calls, tests.last, tests.err, result.Runtime)
	}
	if result.Usage.TotalTokens != 60 || result.Usage.CachedPromptTokens != 10 || result.Usage.ReasoningTokens != 6 || result.Runtime.Accounting.InputTokenBound != payloadBytes.Load()+1024 {
		t.Fatal("native usage or final-wire byte admission was not persisted accurately")
	}
	receipts, err := f.store.ListRepairToolReceipts(ctx, coderepair.ToolReceiptClaim{Job: job, ExpectedVersion: repairCase.Version})
	if err != nil || len(receipts) != 3 {
		t.Fatalf("durable tool proof count=%d err=%v", len(receipts), err)
	}
	for _, receipt := range receipts {
		if receipt.State != "completed" || !strings.HasPrefix(receipt.ResultSealed, "v1.") || strings.Contains(receipt.ResultSealed, "version !=") {
			t.Fatal("durable native tool result was incomplete or plaintext")
		}
	}
	var checkpoints int
	if err := f.admin.QueryRow(`SELECT count(*) FROM `+f.checkpoint+`.checkpoints WHERE thread_id=$1`, attempt.Runtime.ThreadID).Scan(&checkpoints); err != nil || checkpoints < 1 {
		t.Fatalf("native graph did not persist checkpoints: count=%d err=%v", checkpoints, err)
	}
	// Simulate death after external/tool result commits but before authoritative
	// workflow outcome. Reopen the application pool with the same durable scope.
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, w.RootPath(), "checkout", "--", "internal/workload/worker.go")
	reopened, err := postgres.NewPostgresStore(f.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	afterLedger, _ := admission.NewService(reopened.ModelBudgetStore())
	runner.Store, runner.Ledger = reopened, afterLedger
	handler, err := agent.NewHandler(reopened, runner, func(_ context.Context, _ coderepair.RepositoryBinding, requestedSHA string) (string, func() error, error) {
		if requestedSHA != base {
			return "", nil, fmt.Errorf("fixture requested an unapproved base")
		}
		return w.RootPath(), func() error { return nil }, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.Handle(ctx, job); err != nil {
		t.Fatal(err)
	}
	if ok, err := reopened.CompleteRepairJob(ctx, job.ID, job.LeaseToken, time.Now().UTC()); err != nil || !ok {
		t.Fatalf("durable recovered outcome completion: ok=%v err=%v", ok, err)
	}
	loadedCase, err := reopened.GetRepairCase(ctx, repairCase.ID)
	if err != nil || loadedCase.State != coderepair.StatePatchReady || calls.Load() != 2 || tests.calls != 2 {
		loadedAttempt, attemptErr := reopened.GetRepairAttempt(ctx, attempt.ID)
		t.Fatalf("restart reran model/tests or lost final proof: state=%s code=%s reason=%s evidence_age=%s models=%d tests=%d err=%v attempt_error=%v", loadedCase.State, loadedAttempt.ErrorCode, loadedAttempt.ErrorMessage, time.Since(snapshot.CapturedAt), calls.Load(), tests.calls, err, attemptErr)
	}
	storedPatch, err := reopened.GetRepairArtifactContent(ctx, attempt.ID, "patch")
	if err != nil || string(storedPatch) != patch {
		t.Fatal("recovered patch bytes were not stored as an authoritative artifact")
	}
	report, err := reopened.GetRepairArtifactContent(ctx, attempt.ID, "investigation_report")
	if err != nil || strings.Contains(string(report), "synthetic-native-model-key") || strings.Contains(string(report), "synthetic-native-checkpoint-password") {
		t.Fatal("persisted outcome lost its report or leaked private configuration")
	}
	var recorded struct {
		Status  coderepair.State    `json:"status"`
		Usage   ai.CompletionUsage  `json:"usage"`
		Runtime *agent.RuntimeTrace `json:"runtime"`
	}
	if json.Unmarshal(report, &recorded) != nil || recorded.Status != coderepair.StatePatchReady || recorded.Usage.TotalTokens != 60 || recorded.Runtime == nil || recorded.Runtime.Accounting.ModelRequests != 2 {
		t.Fatal("native usage/proof trace was not retained through workflow outcome persistence")
	}
	var downstream map[string]any
	if !t.Run("TestNativeProofRemainsPublishableThroughDeploymentVerification", func(t *testing.T) {
		downstream = nativeProofPublicationAndVerification(t, reopened, repairCase.ID, w.RootPath())
	}) {
		return
	}
	if calls.Load() != 2 || tests.calls != 2 {
		t.Fatal("publication or deployment reran model inference or native tests")
	}
	evidence := map[string]any{"scenario": "persisted-native-terminal-recovery", "status": "passed", "profile": "offline-fixture",
		"provider": "scripted-loopback", "live_model_quality_assessed": false, "paid_model_requests": 0,
		"model_requests": calls.Load(), "docker_tests": tests.calls, "completed_tool_receipts": len(receipts), "checkpoints": checkpoints,
		"provider_reported_fixture_tokens": recorded.Usage.TotalTokens, "serialized_request_bytes": []int64{requestBytes[0].Load(), requestBytes[1].Load()},
		"cumulative_serialized_payload_bytes": payloadBytes.Load(), "admitted_input_bound": result.Runtime.Accounting.InputTokenBound,
		"fixture_input_bound_per_request": budget.MaxInputTokens, "fixture_cumulative_input_bound": budget.Campaign.MaxInputTokens,
		"restart_additional_model_requests": 0, "restart_additional_test_runs": 0, "downstream_publication_and_verification": downstream,
		"patch_sha256": result.PatchReport.SHA256, "recorded_at": time.Now().UTC().Format(time.RFC3339Nano)}
	encoded, _ := json.Marshal(evidence)
	t.Logf("offline native evidence: %s", encoded)
	if path := os.Getenv("TEST_NATIVE_EVIDENCE_PATH"); path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal("offline evidence directory could not be created")
		}
		if err := os.WriteFile(path, append(encoded, '\n'), 0600); err != nil {
			t.Fatal("offline evidence report could not be saved")
		}
	}
}
