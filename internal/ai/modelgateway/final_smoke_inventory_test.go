package modelgateway

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
)

func withWriterTools(t *testing.T, body []byte, names []string) []byte {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	tools := make([]any, 0, len(names))
	for _, name := range names {
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
			"name": name, "parameters": map[string]any{"type": "object"}}})
	}
	value["tools"] = tools
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestFinalSmokeWriterToolInventoryIsExactAndDefaultsUnchanged(t *testing.T) {
	if WriterToolsForProfile("internal") != nil || WriterToolsForProfile("offline-fixture") != nil {
		t.Fatal("ordinary profiles lost their existing product inventory")
	}
	tools := WriterToolsForProfile("final-smoke")
	want := []string{"repo_read", "propose_patch", "cannot_determine", "read_file"}
	if !reflect.DeepEqual(tools, want) {
		t.Fatal("final validation inventory changed", tools)
	}
	tools[0] = "repo_search"
	if !reflect.DeepEqual(WriterToolsForProfile("final-smoke"), want) {
		t.Fatal("caller mutated the pinned inventory")
	}
	for _, scenario := range []struct {
		name  string
		tools []string
		valid bool
	}{
		{"exact inventory", want, true},
		{"missing capability", want[:3], false},
		{"extra discovery", append(append([]string{}, want...), "repo_list"), false},
		{"replaced discovery", []string{"repo_search", "propose_patch", "cannot_determine", "read_file"}, false},
		{"excluded test request", []string{"repo_read", "propose_patch", "cannot_determine", "run_test_recipe"}, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			body := withWriterTools(t, modelPayload("fixture"), scenario.tools)
			if (validateStageTools(body, want) == nil) != scenario.valid {
				t.Fatal("native inventory did not match the approved profile")
			}
		})
	}
}

func TestFinalSmokeExcludedToolIsBlockedBeforeAdmission(t *testing.T) {
	var calls atomic.Int32
	gateway, ledger := gatewayFixture(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	config := gateway.config
	config.AllowedTools = WriterToolsForProfile("final-smoke")
	limited, err := New(config, ledger)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	before, err := ledger.GetCampaign(ctx, config.CampaignID)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"repo_list", "repo_search", "run_test_recipe"} {
		body := withWriterTools(t, modelPayload("denied"), []string{"repo_read", "propose_patch", "cannot_determine", name})
		if _, _, _, err := limited.DispatchAt(ctx, 1, body); err == nil {
			t.Fatal("excluded tool reached provider dispatch", name)
		}
	}
	after, err := ledger.GetCampaign(ctx, config.CampaignID)
	if err != nil || calls.Load() != 0 || !reflect.DeepEqual(before, after) {
		t.Fatal("excluded inventory consumed provider requests or campaign allowance", err)
	}
}
