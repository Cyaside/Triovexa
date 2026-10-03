package modelgateway

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestWireContractRejectsHiddenSpendAndAmbiguousFields(t *testing.T) {
	for _, scenario := range []struct {
		name string
		body string
	}{
		{"duplicate model", `{"model":"expensive-model","model":"fixture-model","messages":[{"role":"user","content":"test"}],"max_tokens":1500}`},
		{"duplicate output cap", `{"model":"fixture-model","messages":[{"role":"user","content":"test"}],"max_tokens":999999,"max_tokens":1500}`},
		{"duplicate role", `{"model":"fixture-model","messages":[{"role":"system","role":"user","content":"test"}],"max_tokens":1500}`},
		{"duplicate native arguments", `{"model":"fixture-model","messages":[{"role":"assistant","tool_calls":[{"id":"x","type":"function","function":{"name":"repo_read","arguments":"{\"path\":\"private\",\"path\":\"allowed\"}"}}]},{"role":"tool","tool_call_id":"x","content":"test"}],"max_tokens":1500}`},
		{"streaming", `{"model":"fixture-model","messages":[{"role":"user","content":"test"}],"max_tokens":1500,"stream":true}`},
		{"parallel dispatch", `{"model":"fixture-model","messages":[{"role":"user","content":"test"}],"max_tokens":1500,"parallel_tool_calls":true}`},
		{"unbounded reasoning", `{"model":"fixture-model","messages":[{"role":"user","content":"test"}],"max_tokens":1500,"reasoning_effort":"high"}`},
		{"output multiplier", `{"model":"fixture-model","messages":[{"role":"user","content":"test"}],"max_tokens":1500,"n":3}`},
		{"agent delegation", `{"model":"fixture-model","messages":[{"role":"user","content":"test"}],"max_tokens":1500,"tools":[{"type":"function","function":{"name":"task"}}]}`},
		{"shell tool", `{"model":"fixture-model","messages":[{"role":"user","content":"test"}],"max_tokens":1500,"tools":[{"type":"function","function":{"name":"execute"}}]}`},
		{"duplicate tool inventory", `{"model":"fixture-model","messages":[{"role":"user","content":"test"}],"max_tokens":1500,"tools":[{"type":"function","function":{"name":"repo_read"}},{"type":"function","function":{"name":"repo_read"}}]}`},
		{"missing output cap", `{"model":"fixture-model","messages":[{"role":"user","content":"test"}]}`},
		{"trailing payload", `{"model":"fixture-model","messages":[{"role":"user","content":"test"}],"max_tokens":1500}{}`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			gateway, ledger := gatewayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid final wire body reached provider") })
			if _, _, _, err := gateway.DispatchAt(context.Background(), 1, []byte(scenario.body)); err == nil {
				t.Fatal("invalid contract was admitted")
			}
			campaign, _ := ledger.GetCampaign(context.Background(), gateway.config.CampaignID)
			if campaign.Requests != 0 || campaign.AdmittedInputTokens != 0 {
				t.Fatal("invalid contract reserved allowance")
			}
		})
	}
}

func TestWireIncludesNativeReasoningAndTextWithoutRenamingModel(t *testing.T) {
	body := []byte(`{"model":"fixture-model","messages":[{"role":"user","content":[{"type":"text","text":"incident context"}]},{"role":"assistant","content":null,"reasoning_content":"native provider reasoning","tool_calls":[{"id":"read-1","type":"function","function":{"name":"repo_read","arguments":"{\"path\":\"internal/workload/worker.go\",\"start_line\":1,\"end_line\":20}"}}]},{"role":"tool","tool_call_id":"read-1","content":"source excerpt"}],"tools":[{"type":"function","function":{"name":"repo_read","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}],"max_tokens":1500,"stream":false,"parallel_tool_calls":false}`)
	if err := ValidatePayload(body, "fixture-model", 1500); err != nil {
		t.Fatalf("native reasoning/tool transcript rejected: %v", err)
	}
	if err := ValidatePayload(body, "replacement-model", 1500); err == nil {
		t.Fatal("native wire changed the pinned model")
	}
}

func TestUnambiguousJSONBoundsNesting(t *testing.T) {
	if err := unambiguousJSON([]byte(strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66))); err == nil {
		t.Fatal("unbounded JSON nesting accepted")
	}
}
