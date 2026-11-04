package runtimebridge

import (
	"context"
	"encoding/json"
	"testing"
)

func TestStructuredProposalRejectsRawAndAmbiguousContracts(t *testing.T) {
	for name, raw := range map[string]string{
		"raw diff":            `{"patch":"diff --git a/x b/x","hypothesis":"a cause","evidence_ids":["log-1"]}`,
		"missing replacement": `{"format":"text-edits-v1","files":[{"path":"internal/workload/worker.go","expected_digest":"abc","edits":[{"old_text":"old"}]}],"hypothesis":"a cause","evidence_ids":["log-1"]}`,
		"unknown edit field":  `{"format":"text-edits-v1","files":[{"path":"internal/workload/worker.go","expected_digest":"abc","edits":[{"old_text":"old","new_text":"fixed","command":"run"}]}],"hypothesis":"a cause","evidence_ids":["log-1"]}`,
		"duplicate key":       `{"format":"text-edits-v1","format":"raw-diff","files":[],"hypothesis":"a cause","evidence_ids":["log-1"]}`,
		"wrong format":        `{"format":"raw-diff","files":[],"hypothesis":"a cause","evidence_ids":["log-1"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			h := newReceiptHarness(t)
			s := h.session(t, true)
			response := s.Execute(context.Background(), ToolRequest{CallID: "invalid-proposal", Name: "propose_patch", Args: json.RawMessage(raw)})
			if s.Result().Patch != "" || s.candidates != 0 || h.tests.calls != 1 || (response.Status != "error" && s.Result().Code != "PROVIDER_CONTRACT_INVALID") {
				t.Fatalf("invalid contract reached candidate: %+v", response)
			}
		})
	}
}
