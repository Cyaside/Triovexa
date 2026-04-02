package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMistralClientCompleteJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Fatalf("authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"summary\":\"ok\"}"}}]}`))
	}))
	defer server.Close()

	client := NewMistralClient("secret", "mistral-small-latest")
	client.baseURL = server.URL

	content, err := client.CompleteJSON(context.Background(), []ChatMessage{
		{Role: "user", Content: "Return JSON"},
	})
	if err != nil {
		t.Fatalf("CompleteJSON returned error: %v", err)
	}
	if content != `{"summary":"ok"}` {
		t.Fatalf("CompleteJSON content = %q", content)
	}
}
