package modelgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/Cyaside/Triovexa/internal/ai/admission"
)

func TestReadOnlyReviewerUsesSharedCampaignAndCannotAddWriterTools(t *testing.T) {
	var calls atomic.Int32
	gateway, ledger := gatewayFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"model":"fixture-model","usage":{"prompt_tokens":20,"completion_tokens":10,"total_tokens":30},"choices":[]}`)
	})
	ctx := context.Background()
	if err := ledger.CreateCampaign(ctx, admission.Campaign{ID: "review-campaign", Profile: "offline-fixture", Offline: true, MaxRequests: 2, MaxInputTokens: 20000, MaxSpendMicroUSD: 200000}); err != nil {
		t.Fatal(err)
	}
	config := gateway.config
	config.CampaignID = "review-campaign"
	config.Phase = "repair"
	writer, err := New(config, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := writer.DispatchAt(ctx, 1, modelPayload("writer")); err != nil {
		t.Fatal(err)
	}
	config.Phase = "reviewer"
	config.AllowedTools = []string{"read_file"}
	reviewer, err := New(config, ledger)
	if err != nil {
		t.Fatal(err)
	}
	var bad map[string]any
	_ = json.Unmarshal(modelPayload("review"), &bad)
	bad["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "propose_patch", "parameters": map[string]any{"type": "object"}}}}
	encoded, _ := json.Marshal(bad)
	if _, _, _, err := reviewer.DispatchAt(ctx, 1, encoded); err == nil || calls.Load() != 1 {
		t.Fatal("reviewer acquired writer rights", err)
	}
	if _, _, _, err := reviewer.DispatchAt(ctx, 1, modelPayload("review")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := reviewer.DispatchAt(ctx, 2, modelPayload("second review")); !errors.Is(err, admission.ErrBudgetExceeded) || calls.Load() != 2 {
		t.Fatal("reviewer reset shared budget", err, calls.Load())
	}
}
