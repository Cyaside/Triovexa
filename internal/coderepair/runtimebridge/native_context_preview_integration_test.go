package runtimebridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/ai/admission"
	"github.com/Cyaside/Triovexa/internal/ai/modelgateway"
	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/agent"
	"github.com/Cyaside/Triovexa/internal/secretstore"
	"github.com/google/uuid"
)

type contextPreviewWire struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	Tools     []struct {
		Function struct{ Name string } `json:"function"`
	} `json:"tools"`
	Messages []struct {
		Role      string          `json:"role"`
		CallID    string          `json:"tool_call_id"`
		Content   json.RawMessage `json:"content"`
		Reasoning string          `json:"reasoning_content"`
		ToolCalls []struct {
			ID       string `json:"id"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	} `json:"messages"`
}

func contextPreviewText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []struct{ Text string }
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	for _, block := range blocks {
		text += block.Text
	}
	return text
}

type contextPreviewCall struct {
	id, name string
	args     map[string]any
}

func contextPreviewCompletion(ordinal int, calls []contextPreviewCall) map[string]any {
	tools := make([]any, 0, len(calls))
	for _, call := range calls {
		args, _ := json.Marshal(call.args)
		tools = append(tools, map[string]any{"id": call.id, "type": "function", "function": map[string]any{"name": call.name, "arguments": string(args)}})
	}
	message := map[string]any{"role": "assistant", "content": nil, "tool_calls": tools}
	if ordinal == 1 {
		// Match the acknowledged live response's sizes without copying its text.
		message["reasoning_content"], message["content"] = strings.Repeat("r", 577), strings.Repeat("v", 109)
	} else if ordinal == 2 {
		message["reasoning_content"] = strings.Repeat("s", 64)
	}
	return map[string]any{"id": fmt.Sprintf("offline-context-%d", ordinal), "model": "glm-5.3-flash", "object": "chat.completion", "created": 1,
		"choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "message": message}},
		"usage": map[string]any{"prompt_tokens": 1190, "completion_tokens": 270, "total_tokens": 1460,
			"completion_tokens_details": map[string]int{"reasoning_tokens": 122}}}
}

// The provider remains a loopback script. Real PostgreSQL, Node and Docker
// exercise the same four-tool profile, template preview and shared ledger used
// by the final smoke; no live credential, shared user database or API is used.
func TestNativeContextPreviewRejectsIrreducibleSkillBatchIntegration(t *testing.T) {
	f := persistedNativeFixture(t)
	w, binding, base := compatibilityFixture(t)
	const patch = "diff --git a/internal/workload/worker.go b/internal/workload/worker.go\n--- a/internal/workload/worker.go\n+++ b/internal/workload/worker.go\n@@ -1,4 +1,4 @@\n package workload\n \n // Schemas 1 and 2 are supported; other versions must be rejected.\n-func rejects(version int) bool { return version != 1 }\n+func rejects(version int) bool { return version != 1 && version != 2 }\n"
	var snapshot coderepair.EvidenceSnapshot
	var mu sync.Mutex
	var bounds []int64
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(request.Body, MaxFrameBytes+1))
		var wire contextPreviewWire
		bound, boundErr := modelgateway.GLMFlashInputBound(raw)
		if err != nil || json.Unmarshal(raw, &wire) != nil || boundErr != nil || bound > 6000 {
			t.Errorf("script received an invalid or oversized template: bytes=%d bound=%d", len(raw), bound)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		bounds = append(bounds, bound)
		ordinal := len(bounds)
		mu.Unlock()
		if request.Header.Get("Authorization") != "Bearer synthetic-context-preview-key" || request.Header.Get("X-Triovexa-Context-Preview") != "" {
			t.Error("private preview contacted the provider or credential scope changed")
		}
		names := make([]string, 0, len(wire.Tools))
		for _, tool := range wire.Tools {
			names = append(names, tool.Function.Name)
		}
		slices.Sort(names)
		if wire.Model != "glm-5.3-flash" || wire.MaxTokens != 1500 || !slices.Equal(names, []string{"cannot_determine", "propose_patch", "read_file", "repo_read"}) {
			t.Error("final-smoke model, output cap or tool inventory changed")
		}
		if ordinal == 2 {
			firstAssistant := -1
			for index, message := range wire.Messages {
				if message.Role == "assistant" {
					firstAssistant = index
					break
				}
			}
			if firstAssistant < 0 || len(wire.Messages[firstAssistant].Reasoning) != 577 || len(contextPreviewText(wire.Messages[firstAssistant].Content)) != 109 || len(wire.Messages[firstAssistant].ToolCalls) != 4 {
				t.Error("bounded view dropped the original reasoning, visible text or native call batch")
			}
		}
		var calls []contextPreviewCall
		switch ordinal {
		case 1:
			for index, path := range []string{"/skills/repository-investigation/SKILL.md", "/skills/incident-evidence/SKILL.md", "/skills/regression-patch/SKILL.md", "internal/workload/worker_regression_test.go"} {
				calls = append(calls, contextPreviewCall{fmt.Sprintf("context-skill-%d", index), "read_file", map[string]any{"file_path": path, "offset": 0, "limit": 200}})
			}
		case 2:
			calls = []contextPreviewCall{{"context-source", "repo_read", map[string]any{"path": "internal/workload/worker.go", "start_line": 1, "end_line": 20}}}
		case 3:
			latestReasoning := ""
			pairedSource := false
			for _, message := range wire.Messages {
				if message.Role == "assistant" {
					latestReasoning = message.Reasoning
					for _, call := range message.ToolCalls {
						if call.ID == "context-source" && call.Function.Name == "repo_read" {
							pairedSource = true
						}
					}
				}
			}
			if latestReasoning != strings.Repeat("s", 64) {
				t.Error("bounded view dropped the current source-read reasoning")
			}
			latest := wire.Messages[len(wire.Messages)-1]
			if !pairedSource || latest.Role != "tool" || latest.CallID != "context-source" || !strings.Contains(contextPreviewText(latest.Content), "version != 1") {
				t.Error("bounded view discarded the newly requested source instead of older tool content")
			}
			ids := make([]string, 0, len(snapshot.Entries))
			for _, evidence := range snapshot.Entries {
				ids = append(ids, evidence.ID)
			}
			calls = []contextPreviewCall{{"context-patch", "propose_patch", structuredArgs(t, w.RootPath(), patch, "The version predicate rejects supported schema two jobs", ids)}}
		default:
			t.Error("native runtime issued an unplanned model request")
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(contextPreviewCompletion(ordinal, calls))
	}))
	t.Cleanup(upstream.Close)
	provider := ai.ProviderConfig{Name: "openai-compatible", BaseURL: upstream.URL, Model: "glm-5.3-flash", APIKey: "synthetic-context-preview-key", AllowHTTP: true, Timeout: 5 * time.Second}
	client, err := ai.NewOpenAICompatibleClient(provider)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secretstore.NewCipher("", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	budget := modelgateway.BudgetConfig{Campaign: admission.Campaign{ID: "context-preview-" + uuid.NewString(), Profile: "final-smoke", MaxRequests: 6, MaxSpendMicroUSD: 200000, MaxInputTokens: 12000},
		Pricing: admission.Pricing{Version: "synthetic-context-v1", Provider: provider.Name, Model: provider.Model, Verified: true, InputBoundVerified: true,
			InputContract: modelgateway.GLMFlashInputContract, BillableOutputBound: true, InputMicroUSDPerMillion: 150000, OutputMicroUSDPerMillion: 500000},
		ConfigVersion: "synthetic-context-v1", MaxInputTokens: 6000, MaxOutputTokens: 1500}
	selection, err := SealSelection(client, budget, cipher)
	if err != nil {
		t.Fatal(err)
	}
	job, repairCase, attempt, snapshot := approveNativeFixture(t, f.store, binding, base, strings.Repeat("a", 40), selection)
	ledger, _ := admission.NewService(f.store.ModelBudgetStore())
	tests := &persistedDockerTester{image: f.image}
	runner := &NativeRunner{Process: Process{Executable: f.node, Entry: f.entry, Version: EngineVersion}, Tests: tests, Store: f.store, Ledger: ledger, Cipher: cipher, CheckpointDSN: f.checkpointDSN, CheckpointSchema: f.checkpoint, MaxModelRequests: 3}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	claimed := agent.ClaimedInvestigation{Job: job, Case: repairCase, Attempt: attempt, ExpectedVersion: repairCase.Version}
	result := runner.Investigate(agent.WithClaimedInvestigation(ctx, claimed), w, binding, snapshot, selection, "go-test-workload")
	mu.Lock()
	measured := slices.Clone(bounds)
	mu.Unlock()
	// The structured edit schema makes this retained four-call/reasoning batch
	// irreducible below the historical 6K cap. Preserve that cap and prove an
	// honest stop instead of discarding scope/thoughts or relaxing admission.
	if result.Status != coderepair.StateBlocked || result.Code != "CONTEXT_LIMIT" || len(measured) != 1 || tests.calls != 1 || result.Patch != "" || result.After.RecipeID != "" || result.Runtime == nil || result.Runtime.Accounting.ModelRequests != 1 {
		t.Fatalf("irreducible batch bypassed cap: status=%s code=%s calls=%d tests=%d", result.Status, result.Code, len(measured), tests.calls)
	}
	campaign, err := ledger.GetCampaign(ctx, budget.Campaign.ID)
	if err != nil || campaign.Requests != 1 || campaign.AdmittedInputTokens != 1190 || campaign.ReservedMicroUSD != 0 || campaign.Blocked || campaign.MaxInputTokens != 12000 || campaign.MaxRequests != 6 {
		t.Fatal("local context previews charged inference or changed campaign limits")
	}
	var preserved int
	if err := f.admin.QueryRow(`SELECT count(*) FROM `+f.checkpoint+`.checkpoint_blobs WHERE thread_id=$1 AND channel='messages' AND position(convert_to($2,'UTF8') IN blob)>0`, attempt.Runtime.ThreadID, strings.Repeat("r", 577)).Scan(&preserved); err != nil || preserved == 0 {
		t.Fatal("context guard discarded original model reasoning")
	}
	outcome, err := result.Outcome(job, repairCase.Version, "go-test-workload", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := f.store.RecordRepairInvestigationOutcome(ctx, outcome); err != nil || !ok {
		t.Fatal("blocked outcome was not retained")
	}
	t.Logf("irreducible context: model_requests=1 baseline_tests=1 candidate_tests=0 bounds=%v; original 6K cap unchanged", measured)
}
