package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/ai/admission"
	"github.com/Cyaside/Triovexa/internal/coderepair"
)

type baselineCall struct {
	PayloadBytes           int   `json:"payload_bytes"`
	MessageCount           int   `json:"message_count"`
	ConservativeInputBound int64 `json:"conservative_input_bound"`
}
type baselineModel struct {
	model *scriptedCompleter
	calls []baselineCall
}

func (m *baselineModel) CompleteJSONDetailed(ctx context.Context, messages []ai.ChatMessage) (ai.CompletionResult, error) {
	payload, err := json.Marshal(map[string]any{"model": "fixture-model", "messages": messages, "response_format": map[string]string{"type": "json_object"}})
	if err != nil {
		return ai.CompletionResult{}, err
	}
	bound, err := admission.ConservativeInputBound(payload, 128)
	if err != nil {
		return ai.CompletionResult{}, err
	}
	m.calls = append(m.calls, baselineCall{len(payload), len(messages), bound})
	return m.model.CompleteJSONDetailed(ctx, messages)
}

// This measures the old Go loop with deterministic responses, not live model
// quality. CI can compare its payload/call shape with the native wire fixture.
func TestAgentLoopOfflineBaseline(t *testing.T) {
	workspace, binding, snapshot, root := loopFixture(t)
	patch := fixturePatch(t, root, "internal/workload/repair_fixture.go")
	model := &baselineModel{model: &scriptedCompleter{decisions: []string{
		`{"operation":"list_files","prefix":"internal/workload"}`,
		`{"operation":"read_file","path":"internal/workload/repair_fixture.go"}`,
		`{"operation":"search_text","prefix":"internal/workload","query":"version"}`,
		`{"operation":"propose_patch","hypothesis":"Schema 2 jobs fail because the version check rejects them","evidence_ids":["log-1","metric-1"],"patch":` + jsonString(t, patch) + `}`,
	}}}
	loop, err := NewLoop(model, &sourceAwareTester{})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	result := loop.Investigate(context.Background(), workspace, binding, snapshot, coderepair.AgentSelection{Provider: "openai-compatible", Model: "fixture-model", PromptVersion: PromptVersion}, "go-test-workload")
	elapsed := time.Since(started)
	if result.Status != coderepair.StatePatchReady || len(model.calls) != 4 {
		t.Fatalf("scripted baseline failed: status=%s code=%s calls=%d", result.Status, result.Code, len(model.calls))
	}
	totalBytes := 0
	totalBound := int64(0)
	for _, call := range model.calls {
		totalBytes += call.PayloadBytes
		totalBound += call.ConservativeInputBound
	}
	if model.calls[3].PayloadBytes <= model.calls[0].PayloadBytes {
		t.Fatal("baseline no longer represents append-history growth")
	}
	report := struct {
		Schema                 int            `json:"schema_version"`
		Corpus                 string         `json:"corpus"`
		Engine                 string         `json:"engine"`
		ModelRequests          int            `json:"model_requests"`
		InputPayloadBytes      int            `json:"input_payload_bytes"`
		ConservativeInputBound int64          `json:"conservative_input_bound"`
		ElapsedNS              int64          `json:"elapsed_ns"`
		ProviderCalls          int            `json:"external_provider_calls"`
		QualityEvidence        string         `json:"quality_evidence"`
		Calls                  []baselineCall `json:"calls"`
	}{1, "repair-context-v1", "legacy-json-loop", len(model.calls), totalBytes, totalBound, int64(elapsed), 0, "scripted control flow only; test runner fixture; no model quality claim", model.calls}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if output := os.Getenv("AI_RUNTIME_BASELINE_REPORT"); output != "" {
		if !filepath.IsAbs(output) {
			t.Fatal("baseline artifact path must be absolute")
		}
		if err = os.MkdirAll(filepath.Dir(output), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(output, encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(string(encoded))
}
