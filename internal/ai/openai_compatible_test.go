package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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

func TestProviderInvalidContentRetainsUsageWithoutRetry(t *testing.T) {
	for _, content := range []string{`"not-json"`, `""`, `{ "unexpected": true }`} {
		t.Run(content, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_, _ = w.Write([]byte(`{"model":"fixture-model","choices":[{"message":{"content":` + content + `}}],"usage":{"prompt_tokens":8,"completion_tokens":5,"total_tokens":13}}`))
			}))
			defer server.Close()
			client, err := NewOpenAICompatibleClient(ProviderConfig{BaseURL: server.URL, APIKey: "dummy", Model: "fixture-model", AllowHTTP: true})
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.CompleteJSONDetailed(t.Context(), []ChatMessage{{Role: "user", Content: "fixture"}})
			if err == nil || result.UsageStatus != UsageKnown || result.Usage.TotalTokens != 13 || calls.Load() != 1 {
				t.Fatalf("invalid output lost usage or retried: result=%+v err=%v calls=%d", result, err, calls.Load())
			}
		})
	}
}

func TestProviderMissingAndInvalidUsageAreExplicit(t *testing.T) {
	for name, fixture := range map[string]struct {
		raw    string
		status UsageStatus
		total  int
	}{
		"absent":       {"", UsageMissing, 0},
		"partial":      {`,"usage":{"total_tokens":13}`, UsageMissing, 13},
		"inconsistent": {`,"usage":{"prompt_tokens":8,"completion_tokens":5,"total_tokens":12}`, UsageInvalid, 12},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]` + fixture.raw + `}`))
			}))
			defer server.Close()
			client, err := NewOpenAICompatibleClient(ProviderConfig{BaseURL: server.URL, APIKey: "dummy", Model: "fixture-model", AllowHTTP: true})
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.CompleteJSONDetailed(t.Context(), nil)
			if err != nil || result.UsageStatus != fixture.status || result.Usage.TotalTokens != fixture.total {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestProviderRejectsCredentialBearingURL(t *testing.T) {
	for _, address := range []string{"https://user:dummy@example.invalid/v1", "https://example.invalid/v1?key=dummy", "https://example.invalid/v1#secret"} {
		if _, err := NewOpenAICompatibleClient(ProviderConfig{BaseURL: address, Model: "fixture", APIKey: "dummy"}); err == nil {
			t.Fatalf("accepted unsafe provider root %q", address)
		}
	}
}

func TestOpenAICompatibleClientRejectsNonLoopbackHTTP(t *testing.T) {
	_, err := NewOpenAICompatibleClient(ProviderConfig{BaseURL: "http://example.com/v1", APIKey: "secret", Model: "test", AllowHTTP: true})
	if err == nil {
		t.Fatal("expected insecure remote URL to be rejected")
	}
}

func TestOpenAICompatibleClientSnapshotSurvivesReconfiguration(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"model":"first","choices":[{"message":{"content":"{\"source\":\"first\"}"}}]}`))
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"model":"second","choices":[{"message":{"content":"{\"source\":\"second\"}"}}]}`))
	}))
	defer second.Close()
	client, err := NewOpenAICompatibleClient(ProviderConfig{BaseURL: first.URL, APIKey: "first-key", Model: "first", AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := client.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Reconfigure(ProviderConfig{BaseURL: second.URL, APIKey: "second-key", Model: "second", AllowHTTP: true}); err != nil {
		t.Fatal(err)
	}
	result, err := frozen.CompleteJSONDetailed(context.Background(), []ChatMessage{{Role: "user", Content: "json"}})
	if err != nil || result.Model != "first" || result.Content != `{"source":"first"}` {
		t.Fatalf("snapshot was reconfigured: %+v, %v", result, err)
	}
}
