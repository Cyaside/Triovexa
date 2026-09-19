package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAICompatibleClientNormalizesV1AndValidatesJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"model":"test","choices":[{"message":{"content":"{\"summary\":\"ok\"}"}}],"usage":{"total_tokens":9}}`))
	}))
	defer server.Close()

	client, err := NewOpenAICompatibleClient(ProviderConfig{Name: "openai-compatible", BaseURL: server.URL + "/v1", APIKey: "secret", Model: "test", JSONMode: true, AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.CompleteJSONDetailed(context.Background(), []ChatMessage{{Role: "user", Content: "json"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != `{"summary":"ok"}` || result.Usage.TotalTokens != 9 {
		t.Fatalf("result = %#v", result)
	}
}

func TestOpenAICompatibleClientRejectsNonLoopbackHTTP(t *testing.T) {
	_, err := NewOpenAICompatibleClient(ProviderConfig{BaseURL: "http://example.com/v1", APIKey: "secret", Model: "test", AllowHTTP: true})
	if err == nil {
		t.Fatal("expected insecure remote URL to be rejected")
	}
}
