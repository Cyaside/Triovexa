package http

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/mode"
	"github.com/Cyaside/Triovexa/internal/storage"
	"github.com/Cyaside/Triovexa/internal/telemetry"
)

func TestServerDebugPoliciesExposesCatalog(t *testing.T) {
	t.Parallel()

	repository := storage.NewMemoryStore()
	server := NewServerWithTelemetry(config.Config{
		ServiceName:     "triovexa",
		Environment:     "test",
		HTTPPort:        "0",
		DatabaseURL:     "postgres://test",
		ReadTimeout:     5 * time.Second,
		WriteTimeout:    5 * time.Second,
		IdleTimeout:     5 * time.Second,
		ShutdownTimeout: 5 * time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), repository, nil, nil, nil, telemetry.NewRecorder())

	api := httptest.NewServer(server.Handler)
	defer api.Close()

	response, err := http.Get(api.URL + "/debug/policies")
	if err != nil {
		t.Fatalf("get debug policies: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("debug policies status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	var payload struct {
		Phase   string           `json:"phase"`
		Catalog []map[string]any `json:"catalog"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode debug policies: %v", err)
	}

	if payload.Phase != "phase-07-hardening-release" {
		t.Fatalf("phase = %q, want %q", payload.Phase, "phase-07-hardening-release")
	}
	if len(payload.Catalog) == 0 {
		t.Fatalf("catalog should not be empty")
	}

	var found bool
	for _, item := range payload.Catalog {
		if key, _ := item["key"].(string); key == "pause_demo_queue_consumer" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("catalog should include pause_demo_queue_consumer")
	}
}

func TestServerRuntimeModeEndpointUpdatesModes(t *testing.T) {
	t.Parallel()

	repository := storage.NewMemoryStore()
	runtimeControls := &RuntimeControls{
		Modes: mode.NewManager("heuristic", "demo"),
		Providers: ProviderStatus{
			GrafanaConfigured: true,
			LLMConfigured:     true,
			LLMProvider:       "openai-compatible",
			LLMModel:          "test-model",
			MetricsSourceUID:  "grafanacloud-prom",
			LogsSourceUID:     "grafanacloud-logs",
		},
	}
	server := NewServerWithTelemetry(config.Config{
		ServiceName:     "triovexa",
		Environment:     "test",
		HTTPPort:        "0",
		DatabaseURL:     "postgres://test",
		ReadTimeout:     5 * time.Second,
		WriteTimeout:    5 * time.Second,
		IdleTimeout:     5 * time.Second,
		ShutdownTimeout: 5 * time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), repository, nil, nil, nil, telemetry.NewRecorder(), runtimeControls)

	api := httptest.NewServer(server.Handler)
	defer api.Close()

	payload := strings.NewReader("reasoning_mode=llm&observability_mode=grafana")
	request, err := http.NewRequest(http.MethodPost, api.URL+"/admin/runtime-modes", payload)
	if err != nil {
		t.Fatalf("new runtime mode request: %v", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("post runtime modes: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("runtime mode status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	snapshot := runtimeControls.Modes.Snapshot()
	if snapshot.Reasoning != mode.ReasoningLLM {
		t.Fatalf("reasoning mode = %q, want %q", snapshot.Reasoning, mode.ReasoningLLM)
	}
	if snapshot.Observability != mode.ObservabilityGrafana {
		t.Fatalf("observability mode = %q, want %q", snapshot.Observability, mode.ObservabilityGrafana)
	}
}

func TestServerIncidentWorkbenchShowsRuntimeControls(t *testing.T) {
	t.Parallel()

	repository := storage.NewMemoryStore()
	runtimeControls := &RuntimeControls{
		Modes: mode.NewManager("llm", "grafana"),
		Providers: ProviderStatus{
			GrafanaConfigured: true,
			LLMConfigured:     true,
			LLMProvider:       "openai-compatible",
			LLMModel:          "test-model",
			MetricsSourceUID:  "grafanacloud-prom",
			LogsSourceUID:     "grafanacloud-logs",
		},
	}
	server := NewServerWithTelemetry(config.Config{
		ServiceName:     "triovexa",
		Environment:     "test",
		HTTPPort:        "0",
		DatabaseURL:     "postgres://test",
		ReadTimeout:     5 * time.Second,
		WriteTimeout:    5 * time.Second,
		IdleTimeout:     5 * time.Second,
		ShutdownTimeout: 5 * time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), repository, nil, nil, nil, telemetry.NewRecorder(), runtimeControls)

	api := httptest.NewServer(server.Handler)
	defer api.Close()

	response, err := http.Get(api.URL + "/ui/incidents")
	if err != nil {
		t.Fatalf("get incident workbench: %v", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read workbench body: %v", err)
	}

	if !strings.Contains(string(body), "Triovexa Operator Console") {
		t.Fatalf("workbench body missing React operator console shell")
	}
	connections, err := http.Get(api.URL + "/api/v1/connections")
	if err != nil {
		t.Fatalf("get connection status: %v", err)
	}
	defer connections.Body.Close()
	connectionBody, err := io.ReadAll(connections.Body)
	if err != nil {
		t.Fatalf("read connection status: %v", err)
	}
	for _, expected := range []string{"grafanacloud-prom", "grafanacloud-logs"} {
		if !strings.Contains(string(connectionBody), expected) {
			t.Fatalf("connection status missing %q", expected)
		}
	}
}
