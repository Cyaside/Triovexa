package http

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
	"time"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/ai/admission"
	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/storage/memory"
)

type connectionAdmissionFixture struct {
	called   int
	provider ai.ProviderConfig
	scope    ai.RequestScope
	err      error
}

func (f *connectionAdmissionFixture) Dispatch(ctx context.Context, provider ai.ProviderConfig, body []byte) ([]byte, int, time.Duration, error) {
	f.called++
	f.provider = provider
	f.scope, _ = ai.RequestScopeFrom(ctx)
	var payload struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Model != provider.Model {
		return nil, 0, 0, errors.New("invalid fixture request")
	}
	if f.err != nil {
		return nil, 0, 0, f.err
	}
	response := fmt.Sprintf(`{"model":%q,"choices":[{"message":{"content":"{\"status\":\"ok\"}"}}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`, provider.Model)
	return []byte(response), 200, time.Millisecond, nil
}

func TestReasoningConnectionTestPreservesAdmissionOnUnsavedProfile(t *testing.T) {
	var network atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { network.Add(1); w.WriteHeader(500) }))
	defer upstream.Close()
	client, err := ai.NewOpenAICompatibleClient(ai.ProviderConfig{Name: "openai-compatible", BaseURL: upstream.URL, APIKey: "saved-fixture-key", Model: "saved-model", AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	gate := &connectionAdmissionFixture{}
	client.SetDispatcher(gate)
	mux := http.NewServeMux()
	registerAPIV1(mux, config.Config{Environment: "local", LLMTimeout: time.Second}, memory.NewMemoryStore(), nil, nil, &RuntimeControls{Reasoning: client})
	for _, key := range []string{"", "unsaved-fixture-key"} {
		payload, _ := json.Marshal(map[string]any{"provider": "openai-compatible", "base_url": upstream.URL + "/v1", "model": "unsaved-model", "credential_ref": "ENCRYPTED_LLM_API_KEY", "api_key": key, "json_mode": true})
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/connections/reasoning/test", strings.NewReader(string(payload))))
		if response.Code != 200 || gate.scope.RunID == "" || gate.scope.Phase != "connection-test" || gate.scope.Ordinal != 1 || gate.provider.Model != "unsaved-model" || gate.provider.BaseURL != upstream.URL+"/v1" {
			t.Fatalf("test bypassed scope/current unsaved profile: status=%d scope=%+v model=%s", response.Code, gate.scope, gate.provider.Model)
		}
		expectedKey := key
		if key == "" {
			expectedKey = "saved-fixture-key"
		}
		if gate.provider.APIKey != expectedKey || client.Model() != "saved-model" || strings.Contains(response.Body.String(), "fixture-key") {
			t.Fatal("test altered saved configuration or exposed a key")
		}
	}
	if gate.called != 2 || network.Load() != 0 {
		t.Fatal("connection test used direct transport")
	}
}

func TestReasoningAdmissionErrorsRemainSpecificAndSanitized(t *testing.T) {
	for _, scenario := range []struct {
		err    error
		status int
		code   string
	}{
		{admission.ErrPricingUnknown, 503, "model_admission_unavailable"},
		{admission.ErrBillingUnbounded, 503, "model_admission_unavailable"},
		{admission.ErrBudgetExceeded, 429, "model_budget_exhausted"},
		{admission.ErrUncertain, 409, "model_dispatch_uncertain"},
		{admission.ErrOffline, 403, "offline_model_restricted"},
		{errors.New("provider failed Authorization: Bearer private-fixture-key https://private.invalid?token=secret"), 502, "provider_test_failed"},
	} {
		response := httptest.NewRecorder()
		writeReasoningTestError(response, &ai.CompletionCallError{Provider: "openai-compatible", Cause: scenario.err})
		if response.Code != scenario.status || !strings.Contains(response.Body.String(), scenario.code) || strings.Contains(response.Body.String(), "private") || strings.Contains(response.Body.String(), "token=secret") {
			t.Fatalf("unsafe or imprecise error: %s", response.Body.String())
		}
	}
}
