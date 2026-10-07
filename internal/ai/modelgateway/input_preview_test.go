package modelgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Cyaside/Triovexa/internal/ai/admission"
)

func previewRequest(gateway *Gateway, body []byte) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer "+gateway.Capability())
	request.Header.Set("X-Triovexa-Case-ID", "case")
	request.Header.Set("X-Triovexa-Attempt-ID", "attempt")
	request.Header.Set("X-Triovexa-Request-Ordinal", "1")
	request.Header.Set("X-Triovexa-Context-Preview", "1")
	return request
}

func TestInputPreviewMeasuresOversizeWithoutAdmissionOrProvider(t *testing.T) {
	gateway, ledger := gatewayFixture(t, func(http.ResponseWriter, *http.Request) { t.Fatal("preview contacted provider") })
	body := modelPayload(strings.Repeat("x", 6000))
	before, _ := ledger.GetCampaign(context.Background(), gateway.config.CampaignID)
	for n := 0; n < 2; n++ {
		response := httptest.NewRecorder()
		gateway.ServeHTTP(response, previewRequest(gateway, body))
		var preview InputPreview
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &preview) != nil || preview.InputBound <= preview.MaxInputTokens || preview.MaxInputTokens != 6000 {
			t.Fatalf("oversized context not measured: status=%d body=%s", response.Code, response.Body.String())
		}
	}
	after, _ := ledger.GetCampaign(context.Background(), gateway.config.CampaignID)
	if before != after {
		t.Fatal("read-only preview changed campaign accounting")
	}
	if _, err := ledger.GetRequest(context.Background(), gateway.requestID(1)); !errors.Is(err, admission.ErrNotFound) {
		t.Fatal("preview created a dispatch reservation")
	}
	if _, _, _, err := gateway.DispatchAt(context.Background(), 1, body); !errors.Is(err, ErrContextLimit) {
		t.Fatal("preview bypassed actual dispatch context gate")
	}
}

func TestInputPreviewAppliesPrivateScopeAndPayloadValidation(t *testing.T) {
	gateway, ledger := gatewayFixture(t, func(http.ResponseWriter, *http.Request) { t.Fatal("preview contacted provider") })
	for _, scenario := range []struct {
		name   string
		edit   func(*http.Request)
		status int
	}{
		{"wrong capability", func(r *http.Request) { r.Header.Set("Authorization", "Bearer invalid") }, http.StatusForbidden},
		{"wrong attempt", func(r *http.Request) { r.Header.Set("X-Triovexa-Attempt-ID", "other") }, http.StatusForbidden},
		{"browser origin", func(r *http.Request) { r.Header.Set("Origin", "https://example.invalid") }, http.StatusForbidden},
		{"invalid ordinal", func(r *http.Request) { r.Header.Set("X-Triovexa-Request-Ordinal", "0") }, http.StatusConflict},
		{"unknown preview", func(r *http.Request) { r.Header.Set("X-Triovexa-Context-Preview", "true") }, http.StatusConflict},
		{"duplicate preview", func(r *http.Request) { r.Header.Add("X-Triovexa-Context-Preview", "1") }, http.StatusConflict},
		{"empty preview", func(r *http.Request) { r.Header.Set("X-Triovexa-Context-Preview", "") }, http.StatusConflict},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			request := previewRequest(gateway, modelPayload("test"))
			scenario.edit(request)
			response := httptest.NewRecorder()
			gateway.ServeHTTP(response, request)
			if response.Code != scenario.status {
				t.Fatalf("status=%d", response.Code)
			}
		})
	}
	for _, body := range [][]byte{[]byte(`{"model":"other"}`), []byte(`{"secret":"must-not-be-relayed"}`)} {
		response := httptest.NewRecorder()
		gateway.ServeHTTP(response, previewRequest(gateway, body))
		if response.Code != http.StatusConflict || strings.Contains(response.Body.String(), "must-not-be-relayed") {
			t.Fatal("invalid preview payload accepted or echoed")
		}
	}
	gateway.config.Fence = func(context.Context) error { return errors.New("private failure") }
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, previewRequest(gateway, modelPayload("test")))
	if response.Code != http.StatusConflict || strings.Contains(response.Body.String(), "private failure") {
		t.Fatal("lease fence bypassed or leaked")
	}
	campaign, _ := ledger.GetCampaign(context.Background(), gateway.config.CampaignID)
	if campaign.Requests != 0 {
		t.Fatal("rejected preview reserved a request")
	}
}

func TestInputPreviewCannotBypassCumulativeBudgetAndDoesNotBreakReceiptReplay(t *testing.T) {
	var calls atomic.Int32
	gateway, ledger := gatewayFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"model":"fixture-model","choices":[{"message":{"role":"assistant","content":"{}"}}],"usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24}}`)
	})
	body := modelPayload("test")
	_, _, _, err := gateway.DispatchAt(context.Background(), 1, body)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := ledger.GetCampaign(context.Background(), gateway.config.CampaignID)
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, previewRequest(gateway, body))
	if response.Code != http.StatusOK {
		t.Fatal("accounted replay preview rejected")
	}
	if _, _, _, err := gateway.DispatchAt(context.Background(), 1, body); err != nil || calls.Load() != 1 {
		t.Fatal("preview caused replay dispatch")
	}
	after, _ := ledger.GetCampaign(context.Background(), gateway.config.CampaignID)
	if before != after {
		t.Fatal("preview/replay changed receipt accounting")
	}
	// A different component can consume shared capacity between preview and
	// dispatch. The successful local preview never authorizes that dispatch.
	remaining := after.MaxInputTokens - after.AdmittedInputTokens
	_, err = ledger.Reserve(context.Background(), admission.Request{ID: "other-component", CampaignID: after.ID, AttemptID: "other", Phase: "triage", ConfigVersion: "other", PayloadHash: admission.PayloadHash([]byte("other")), Provider: gateway.config.Provider.Name, Model: gateway.config.Provider.Model, InputTokenBound: remaining, OutputTokenBound: 1}, gateway.config.Pricing)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := gateway.DispatchAt(context.Background(), 2, body); !errors.Is(err, admission.ErrBudgetExceeded) || calls.Load() != 1 {
		t.Fatal("preview bypassed shared admission")
	}
}
