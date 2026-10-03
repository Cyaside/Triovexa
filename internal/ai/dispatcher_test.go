package ai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type fixtureDispatcher struct {
	calls   int
	configs []ProviderConfig
	err     error
}

func (d *fixtureDispatcher) Dispatch(_ context.Context, config ProviderConfig, _ []byte) ([]byte, int, time.Duration, error) {
	d.calls++
	d.configs = append(d.configs, config)
	return []byte(`{"choices":[{"message":{"content":"{}"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`), 200, time.Millisecond, d.err
}

func TestDispatcherFailureCannotBypassAdmission(t *testing.T) {
	var directCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { directCalls.Add(1) }))
	defer server.Close()
	client, err := NewOpenAICompatibleClient(ProviderConfig{BaseURL: server.URL, APIKey: "dummy", Model: "fixture", AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	denied := errors.New("fixture admission denied")
	dispatcher := &fixtureDispatcher{err: denied}
	client.SetDispatcher(dispatcher)
	_, err = client.CompleteJSONDetailed(t.Context(), nil)
	if !errors.Is(err, denied) || dispatcher.calls != 1 || directCalls.Load() != 0 {
		t.Fatalf("admission bypass/retry: err=%v dispatch=%d direct=%d", err, dispatcher.calls, directCalls.Load())
	}
}

func TestSnapshotPinsConfigurationAndPreservesDispatcher(t *testing.T) {
	client, err := NewOpenAICompatibleClient(ProviderConfig{BaseURL: "https://example.invalid/v1", APIKey: "dummy", Model: "first"})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &fixtureDispatcher{}
	client.SetDispatcher(dispatcher)
	frozen, err := client.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Reconfigure(ProviderConfig{BaseURL: "https://another.invalid/v1", Model: "second"}); err != nil {
		t.Fatal(err)
	}
	if _, err := frozen.CompleteJSONDetailed(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CompleteJSONDetailed(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if dispatcher.calls != 2 || dispatcher.configs[0].Model != "first" || dispatcher.configs[1].Model != "second" {
		t.Fatalf("dispatcher/configuration lost: %+v", dispatcher.configs)
	}
}
