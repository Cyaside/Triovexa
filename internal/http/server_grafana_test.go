package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/execution"
	"github.com/Cyaside/Triovexa/internal/incident"
	"github.com/Cyaside/Triovexa/internal/mode"
	"github.com/Cyaside/Triovexa/internal/storage"
	"github.com/Cyaside/Triovexa/internal/telemetry"
)

func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent directories: %v", err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write file %s: %v", path, err)
	}
}

type fakeHTTPAdapter struct{}

func (fakeHTTPAdapter) Execute(_ context.Context, _ domain.CandidateAction, _ execution.AdapterRequest) (execution.AdapterResult, error) {
	return execution.AdapterResult{ExecutorType: "fake-http-adapter"}, nil
}

func newFakeGrafanaSetupServer(t *testing.T) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		switch {
		case r.URL.Path == "/api/datasources":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"uid": "test-prom", "name": "Primary Metrics", "type": "prometheus", "isDefault": true, "readOnly": false},
				{"uid": "test-logs", "name": "Primary Logs", "type": "loki", "isDefault": false, "readOnly": false},
			})
		case strings.Contains(r.URL.Path, "/api/datasources/proxy/uid/test-prom/api/v1/query"):
			value := "0"
			switch r.URL.Query().Get("query") {
			case "vector(0.12)":
				value = "0.12"
			case "vector(220)":
				value = "220"
			case "vector(4)":
				value = "4"
			case "vector(2)":
				value = "2"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{
					"resultType": "vector",
					"result": []map[string]any{
						{"value": []any{float64(time.Now().Unix()), value}},
					},
				},
			})
		case strings.Contains(r.URL.Path, "/api/datasources/proxy/uid/test-logs/loki/api/v1/query_range"):
			lines := [][]string{{"1712088000000000000", "checkout-service timeout log line"}}
			if strings.Contains(r.URL.Query().Get("query"), "deploy") {
				lines = [][]string{{"1712088000000000000", "checkout-service deployment v1.2.3 completed"}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{
					"result": []map[string]any{
						{"values": lines},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

func urlQueryEscape(value string) string {
	return url.QueryEscape(value)
}
func TestServerRuntimeModeEndpointRejectsInvalidModes(t *testing.T) {
	t.Parallel()

	repository := storage.NewMemoryStore()
	runtimeControls := &RuntimeControls{
		Modes: mode.NewManager("heuristic", "demo"),
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

	payload := strings.NewReader("reasoning_mode=wat")
	request, err := http.NewRequest(http.MethodPost, api.URL+"/admin/runtime-modes", payload)
	if err != nil {
		t.Fatalf("new invalid runtime mode request: %v", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("post invalid runtime mode: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("runtime mode invalid status = %d, want %d", response.StatusCode, http.StatusBadRequest)
	}

	snapshot := runtimeControls.Modes.Snapshot()
	if snapshot.Reasoning != mode.ReasoningHeuristic {
		t.Fatalf("reasoning mode changed unexpectedly to %q", snapshot.Reasoning)
	}
}
func TestServerGrafanaWebhookDeduplicatesActiveIncident(t *testing.T) {
	t.Parallel()

	repository := storage.NewMemoryStore()
	incidentService := incident.NewService(repository, nil, nil, nil, nil, nil)
	server := NewServer(config.Config{
		ServiceName:     "triovexa",
		Environment:     "test",
		HTTPPort:        "0",
		DatabaseURL:     "memory",
		ReadTimeout:     5 * time.Second,
		WriteTimeout:    5 * time.Second,
		IdleTimeout:     5 * time.Second,
		ShutdownTimeout: 5 * time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), repository, incidentService, nil, nil)

	api := httptest.NewServer(server.Handler)
	defer api.Close()

	payload := map[string]any{
		"title": "checkout timeout alert",
		"state": "firing",
		"alerts": []map[string]any{{
			"status":      "firing",
			"fingerprint": "alert-dedup-001",
			"startsAt":    time.Now().UTC().Format(time.RFC3339),
			"labels": map[string]string{
				"service":     "checkout-service",
				"environment": "staging",
				"severity":    "critical",
			},
		}},
	}
	body, _ := json.Marshal(payload)

	responseA, err := http.Post(api.URL+"/webhooks/grafana", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post first webhook: %v", err)
	}
	defer responseA.Body.Close()
	var first map[string]any
	if err := json.NewDecoder(responseA.Body).Decode(&first); err != nil {
		t.Fatalf("decode first webhook response: %v", err)
	}

	responseB, err := http.Post(api.URL+"/webhooks/grafana", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post duplicate webhook: %v", err)
	}
	defer responseB.Body.Close()
	var second map[string]any
	if err := json.NewDecoder(responseB.Body).Decode(&second); err != nil {
		t.Fatalf("decode duplicate webhook response: %v", err)
	}

	if first["incident_id"] != second["incident_id"] {
		t.Fatalf("duplicate webhook created a new incident: first=%v second=%v", first["incident_id"], second["incident_id"])
	}

	listResponse, err := http.Get(api.URL + "/incidents")
	if err != nil {
		t.Fatalf("get incidents: %v", err)
	}
	defer listResponse.Body.Close()
	var listPayload struct {
		Incidents []map[string]any `json:"incidents"`
	}
	if err := json.NewDecoder(listResponse.Body).Decode(&listPayload); err != nil {
		t.Fatalf("decode incidents response: %v", err)
	}
	if len(listPayload.Incidents) != 1 {
		t.Fatalf("incident count = %d, want 1 after duplicate webhook", len(listPayload.Incidents))
	}
}

func TestServerGrafanaWebhookIgnoresResolvedAlertWithoutActiveIncident(t *testing.T) {
	t.Parallel()

	repository := storage.NewMemoryStore()
	incidentService := incident.NewService(repository, nil, nil, nil, nil, nil)
	server := NewServer(config.Config{
		ServiceName:     "triovexa",
		Environment:     "test",
		HTTPPort:        "0",
		DatabaseURL:     "memory",
		ReadTimeout:     5 * time.Second,
		WriteTimeout:    5 * time.Second,
		IdleTimeout:     5 * time.Second,
		ShutdownTimeout: 5 * time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), repository, incidentService, nil, nil)

	api := httptest.NewServer(server.Handler)
	defer api.Close()

	payload := map[string]any{
		"title": "checkout timeout alert",
		"state": "resolved",
		"alerts": []map[string]any{{
			"status":      "resolved",
			"fingerprint": "alert-resolved-001",
			"startsAt":    time.Now().UTC().Format(time.RFC3339),
			"labels": map[string]string{
				"service":     "checkout-service",
				"environment": "staging",
				"severity":    "critical",
			},
		}},
	}
	body, _ := json.Marshal(payload)

	response, err := http.Post(api.URL+"/webhooks/grafana", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post resolved webhook: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("resolved webhook status = %d, want %d", response.StatusCode, http.StatusAccepted)
	}

	var result map[string]any
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode resolved webhook response: %v", err)
	}
	if ignored, _ := result["ignored"].(bool); !ignored {
		t.Fatalf("resolved webhook should be ignored, got %v", result)
	}

	listResponse, err := http.Get(api.URL + "/incidents")
	if err != nil {
		t.Fatalf("get incidents: %v", err)
	}
	defer listResponse.Body.Close()
	var listPayload struct {
		Incidents []map[string]any `json:"incidents"`
	}
	if err := json.NewDecoder(listResponse.Body).Decode(&listPayload); err != nil {
		t.Fatalf("decode incidents response: %v", err)
	}
	if len(listPayload.Incidents) != 0 {
		t.Fatalf("incident count = %d, want 0 for ignored resolved alert", len(listPayload.Incidents))
	}
}

func TestServerGrafanaWebhookClosesActiveIncidentOnResolvedAlert(t *testing.T) {
	t.Parallel()

	repository := storage.NewMemoryStore()
	incidentService := incident.NewService(repository, nil, nil, nil, nil, nil)
	server := NewServer(config.Config{
		ServiceName:     "triovexa",
		Environment:     "test",
		HTTPPort:        "0",
		DatabaseURL:     "memory",
		ReadTimeout:     5 * time.Second,
		WriteTimeout:    5 * time.Second,
		IdleTimeout:     5 * time.Second,
		ShutdownTimeout: 5 * time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), repository, incidentService, nil, nil)

	api := httptest.NewServer(server.Handler)
	defer api.Close()

	firingPayload := map[string]any{
		"title": "checkout timeout alert",
		"state": "firing",
		"alerts": []map[string]any{{
			"status":      "firing",
			"fingerprint": "alert-resolved-002",
			"startsAt":    time.Now().UTC().Format(time.RFC3339),
			"labels": map[string]string{
				"service":     "checkout-service",
				"environment": "staging",
				"severity":    "critical",
			},
		}},
	}
	body, _ := json.Marshal(firingPayload)
	response, err := http.Post(api.URL+"/webhooks/grafana", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post firing webhook: %v", err)
	}
	defer response.Body.Close()
	var firingResult map[string]any
	if err := json.NewDecoder(response.Body).Decode(&firingResult); err != nil {
		t.Fatalf("decode firing response: %v", err)
	}
	incidentID, _ := firingResult["incident_id"].(string)

	resolvedPayload := map[string]any{
		"title": "checkout timeout alert",
		"state": "resolved",
		"alerts": []map[string]any{{
			"status":      "resolved",
			"fingerprint": "alert-resolved-002",
			"startsAt":    time.Now().UTC().Format(time.RFC3339),
			"labels": map[string]string{
				"service":     "checkout-service",
				"environment": "staging",
				"severity":    "critical",
			},
		}},
	}
	resolvedBody, _ := json.Marshal(resolvedPayload)
	resolvedResponse, err := http.Post(api.URL+"/webhooks/grafana", "application/json", bytes.NewReader(resolvedBody))
	if err != nil {
		t.Fatalf("post resolved webhook: %v", err)
	}
	defer resolvedResponse.Body.Close()
	var resolvedResult map[string]any
	if err := json.NewDecoder(resolvedResponse.Body).Decode(&resolvedResult); err != nil {
		t.Fatalf("decode resolved response: %v", err)
	}
	if resolvedResult["incident_id"] != incidentID {
		t.Fatalf("resolved webhook should reuse active incident id, got %v want %v", resolvedResult["incident_id"], incidentID)
	}
	if resolvedResult["state"] != string(domain.IncidentStateClosed) {
		t.Fatalf("resolved webhook state = %v, want %q", resolvedResult["state"], domain.IncidentStateClosed)
	}
}

func waitForTriageResult(t *testing.T, baseURL string, incidentID string) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(baseURL + "/incidents/" + incidentID + "/triage")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}

	t.Fatalf("triage result for incident %s did not become ready in time", incidentID)
}

func waitForCandidateActionsReady(t *testing.T, baseURL string, incidentID string, minimum int) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(baseURL + "/incidents/" + incidentID + "/actions")
		if err == nil {
			var payload struct {
				Actions []map[string]any `json:"actions"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&payload)
			response.Body.Close()
			if decodeErr == nil && len(payload.Actions) >= minimum {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}

	t.Fatalf("candidate actions for incident %s did not become ready in time", incidentID)
}
