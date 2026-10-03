package modelgateway

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/ai/admission"
	"github.com/Cyaside/Triovexa/internal/secretstore"
)

func gatewayFixture(t *testing.T, handler http.HandlerFunc) (*Gateway, *admission.Service) {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	ledger, _ := admission.NewService(admission.NewMemoryStore())
	if err := ledger.CreateCampaign(context.Background(), admission.Campaign{ID: "offline-campaign", Profile: "offline-fixture", Offline: true, MaxSpendMicroUSD: 200000, MaxRequests: 6, MaxInputTokens: 12000}); err != nil {
		t.Fatal(err)
	}
	cipher, err := secretstore.NewCipher("", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	provider := ai.ProviderConfig{Name: "openai-compatible", Model: "fixture-model", BaseURL: upstream.URL, APIKey: "dummy-test-key", AllowHTTP: true}
	gateway, err := New(Config{Provider: provider, Pricing: admission.Pricing{Version: "fixture-v1", Provider: provider.Name, Model: provider.Model}, CampaignID: "offline-campaign", CaseID: "case", AttemptID: "attempt", Phase: "repair", ConfigVersion: "config-v1", MaxInputTokens: 6000, MaxOutputTokens: 1500, MaxRequests: 4, Cipher: cipher, Fence: func(ctx context.Context) error { return ctx.Err() }}, ledger)
	if err != nil {
		t.Fatal(err)
	}
	return gateway, ledger
}

func modelPayload(text string) []byte {
	return []byte(fmt.Sprintf(`{"model":"fixture-model","messages":[{"role":"user","content":%q}],"max_tokens":1500,"stream":false}`, text))
}

func TestDispatchReceiptReplaysWithoutProviderCall(t *testing.T) {
	var count atomic.Int32
	gateway, ledger := gatewayFixture(t, func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer dummy-test-key" || r.Header.Get("X-Triovexa-Case-ID") != "" {
			t.Error("private headers leaked or API root duplicated")
		}
		fmt.Fprint(w, `{"model":"fixture-model","choices":[{"message":{"role":"assistant","content":"{}"}}],"usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24}}`)
	})
	body := modelPayload("test")
	first, _, _, err := gateway.DispatchAt(context.Background(), 1, body)
	if err != nil {
		t.Fatal(err)
	}
	second, _, _, err := gateway.DispatchAt(context.Background(), 1, body)
	if err != nil || string(first) != string(second) || count.Load() != 1 {
		t.Fatalf("receipt replay made paid duplicate: %d %v", count.Load(), err)
	}
	request, err := ledger.GetRequest(context.Background(), gateway.requestID(1))
	if err != nil || request.Receipt == nil || strings.Contains(string(request.Receipt.Response), "fixture-model") {
		t.Fatal("private response receipt was not sealed")
	}
	if _, _, _, err := gateway.DispatchAt(context.Background(), 1, modelPayload("changed")); !errors.Is(err, admission.ErrMismatch) {
		t.Fatalf("changed payload replay: %v", err)
	}
}

func TestMissingUsageBlocksNextCall(t *testing.T) {
	var count atomic.Int32
	gateway, _ := gatewayFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		count.Add(1)
		fmt.Fprint(w, `{"model":"fixture-model","choices":[]}`)
	})
	if _, _, _, err := gateway.DispatchAt(context.Background(), 1, modelPayload("test")); !errors.Is(err, admission.ErrUsageInvalid) {
		t.Fatalf("missing usage error: %v", err)
	}
	if _, _, _, err := gateway.DispatchAt(context.Background(), 2, modelPayload("test")); !errors.Is(err, admission.ErrUncertain) || count.Load() != 1 {
		t.Fatal("missing usage triggered another dispatch")
	}
}

func TestMalformedResponseRetainsUsage(t *testing.T) {
	gateway, ledger := gatewayFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"model":"fixture-model","choices":"invalid","usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24}}`)
	})
	if _, _, _, err := gateway.DispatchAt(context.Background(), 1, modelPayload("test")); err != nil {
		t.Fatal(err)
	}
	request, _ := ledger.GetRequest(context.Background(), gateway.requestID(1))
	if request.State != admission.Accounted || request.Receipt.Usage.TotalTokens != 24 {
		t.Fatal("invalid content discarded billing usage")
	}
}

func TestRuntimeOfflineProfileRejectsExternalEgress(t *testing.T) {
	gateway, _ := gatewayFixture(t, func(http.ResponseWriter, *http.Request) { t.Fatal("external dispatch bypassed offline guard") })
	gateway.config.Provider.BaseURL = "https://provider.example.invalid/v1"
	if _, _, _, err := gateway.DispatchAt(context.Background(), 1, modelPayload("test")); !errors.Is(err, admission.ErrOffline) {
		t.Fatalf("offline egress: %v", err)
	}
}

func TestFinalPayloadIncludesToolAndMiddlewareTokens(t *testing.T) {
	gateway, _ := gatewayFixture(t, func(http.ResponseWriter, *http.Request) { t.Fatal("oversized final SDK payload was dispatched") })
	if _, _, _, err := gateway.DispatchAt(context.Background(), 1, modelPayload(strings.Repeat("a", 6000))); err == nil {
		t.Fatal("middleware/content bytes escaped context admission")
	}
}

func TestNativeToolPairingRejectsInvalidCalls(t *testing.T) {
	for _, messages := range []string{
		`[{"role":"tool","tool_call_id":"x","content":"x"}]`,
		`[{"role":"assistant","tool_calls":[{"id":"x","type":"function","function":{"name":"publish","arguments":"{}"}}]}]`,
		`[{"role":"assistant","tool_calls":[{"id":"x","type":"function","function":{"name":"repo_list","arguments":"{}"}}]},{"role":"user","content":"next"}]`,
		`[{"role":"user","content":[{"type":"image_url"}]}]`,
	} {
		body := []byte(`{"model":"fixture-model","messages":` + messages + `,"max_tokens":1500}`)
		if ValidatePayload(body, "fixture-model", 1500) == nil {
			t.Fatalf("accepted invalid native messages %s", messages)
		}
	}
}
