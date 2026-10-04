package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestOpenAICompatibleAPIRootUsesExactlyOneChatCompletionsRoute(t *testing.T) {
	for _, suffix := range []string{"", "/", "///", "/v1", "/v1/", "/v1///"} {
		t.Run(suffix, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.URL.RawQuery != "" {
					t.Errorf("unexpected compatible endpoint: %s %s", r.Method, r.URL.String())
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"model":"endpoint-fixture","choices":[{"message":{"content":"{\"status\":\"ok\"}"}}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`))
			}))
			defer server.Close()
			client, err := NewOpenAICompatibleClient(ProviderConfig{Name: "openai-compatible", BaseURL: server.URL + suffix,
				Model: "endpoint-fixture", APIKey: "synthetic-endpoint-key", AllowHTTP: true})
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.CompleteJSONDetailed(context.Background(), []ChatMessage{{Role: "user", Content: "endpoint fixture"}})
			if err != nil || result.Content != `{"status":"ok"}` || result.UsageStatus != UsageKnown || requests.Load() != 1 {
				t.Fatalf("API root changed the route or retried: result=%+v requests=%d err=%v", result, requests.Load(), err)
			}
		})
	}
}

func TestOpenAICompatibleRedirectNeverForwardsCredentialsOrSwitchesProtocol(t *testing.T) {
	for _, code := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var originalCalls, redirectedCalls atomic.Int32
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				redirectedCalls.Add(1)
				t.Errorf("redirect reached another endpoint with method=%s credential=%t", r.Method, r.Header.Get("Authorization") != "")
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer destination.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				originalCalls.Add(1)
				if r.URL.Path != "/v1/chat/completions" {
					t.Errorf("unexpected original route: %s", r.URL.Path)
				}
				w.Header().Set("Location", destination.URL+"/v1/responses")
				w.WriteHeader(code)
			}))
			defer server.Close()
			client, err := NewOpenAICompatibleClient(ProviderConfig{Name: "openai-compatible", BaseURL: server.URL,
				Model: "endpoint-fixture", APIKey: "synthetic-endpoint-key", AllowHTTP: true})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.CompleteJSONDetailed(context.Background(), []ChatMessage{{Role: "user", Content: "redirect fixture"}})
			if err == nil || originalCalls.Load() != 1 || redirectedCalls.Load() != 0 {
				t.Fatalf("redirect allowed a second dispatch/protocol: original=%d redirected=%d err=%v", originalCalls.Load(), redirectedCalls.Load(), err)
			}
		})
	}
}
