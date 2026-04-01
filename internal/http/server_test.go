package http

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/approval"
	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/demo"
	"github.com/Cyaside/Triovexa/internal/execution"
	"github.com/Cyaside/Triovexa/internal/incident"
	"github.com/Cyaside/Triovexa/internal/observability"
	"github.com/Cyaside/Triovexa/internal/policy"
	"github.com/Cyaside/Triovexa/internal/remediation"
	"github.com/Cyaside/Triovexa/internal/retrieval"
	"github.com/Cyaside/Triovexa/internal/storage"
	"github.com/Cyaside/Triovexa/internal/triage"
)

func TestServerEndToEndReadOnlyTriage(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	docsRoot := filepath.Join(tempDir, "docs")
	mustWriteFile(t, filepath.Join(docsRoot, "runbooks", "restart-demo-worker.md"), "# Restart demo worker\n\nWorker backlog handling.")
	mustWriteFile(t, filepath.Join(docsRoot, "postmortems", "checkout-timeout-after-deploy.md"), "# Checkout timeout after deploy\n\nTimeout observed after deploy.")

	demoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/state" {
			http.NotFound(w, r)
			return
		}

		_ = json.NewEncoder(w).Encode(demo.Snapshot{
			Mode:           demo.ModeTimeoutAfterDeploy,
			ErrorRate:      0.27,
			LatencyMs:      1200,
			QueueBacklog:   40,
			WorkerHealthy:  true,
			LastDeploy:     "v1.2.3",
			LastUpdatedUTC: time.Now().UTC(),
		})
	}))
	defer demoServer.Close()

	repository := storage.NewMemoryStore()

	collector := observability.NewDemoCollector(demoServer.URL)
	retriever := retrieval.NewFileRetriever(docsRoot)
	catalog := execution.DefaultCatalog()
	generator := triage.NewHeuristicGenerator()
	actionGenerator := remediation.NewHeuristicGenerator(catalog)
	policyService := approval.NewService(repository, policy.NewEvaluator(catalog), approval.NewKillSwitch(false))
	incidentService := incident.NewService(repository, collector, retriever, generator, actionGenerator, policyService)

	server := NewServer(config.Config{
		ServiceName:        "triovexa",
		Environment:        "test",
		HTTPPort:           "0",
		DatabaseURL:        "postgres://test",
		DocsRoot:           docsRoot,
		DemoServiceBaseURL: demoServer.URL,
		ReadTimeout:        5 * time.Second,
		WriteTimeout:       5 * time.Second,
		IdleTimeout:        5 * time.Second,
		ShutdownTimeout:    5 * time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), repository, incidentService)

	api := httptest.NewServer(server.Handler)
	defer api.Close()

	payload := map[string]any{
		"title": "checkout timeout after deploy",
		"commonLabels": map[string]string{
			"service":     "checkout-service",
			"environment": "staging",
			"severity":    "critical",
		},
		"alerts": []map[string]any{
			{
				"status":      "firing",
				"fingerprint": "alert-001",
				"startsAt":    time.Now().UTC().Format(time.RFC3339),
				"labels": map[string]string{
					"service":     "checkout-service",
					"environment": "staging",
					"severity":    "critical",
				},
				"annotations": map[string]string{
					"summary": "checkout timeout alert",
				},
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal webhook payload: %v", err)
	}

	webhookResponse, err := http.Post(api.URL+"/webhooks/grafana", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post grafana webhook: %v", err)
	}
	defer webhookResponse.Body.Close()

	if webhookResponse.StatusCode != http.StatusAccepted {
		t.Fatalf("webhook status = %d, want %d", webhookResponse.StatusCode, http.StatusAccepted)
	}

	var webhookPayload map[string]any
	if err := json.NewDecoder(webhookResponse.Body).Decode(&webhookPayload); err != nil {
		t.Fatalf("decode webhook response: %v", err)
	}

	incidentID, ok := webhookPayload["incident_id"].(string)
	if !ok || incidentID == "" {
		t.Fatalf("incident id missing from webhook response")
	}

	state, ok := webhookPayload["state"].(string)
	if !ok || state != "awaiting_approval" {
		t.Fatalf("webhook state = %v, want %q", webhookPayload["state"], "awaiting_approval")
	}

	triageResponse, err := http.Get(api.URL + "/incidents/" + incidentID + "/triage")
	if err != nil {
		t.Fatalf("get triage response: %v", err)
	}
	defer triageResponse.Body.Close()

	if triageResponse.StatusCode != http.StatusOK {
		t.Fatalf("triage status = %d, want %d", triageResponse.StatusCode, http.StatusOK)
	}

	actionsResponse, err := http.Get(api.URL + "/incidents/" + incidentID + "/actions")
	if err != nil {
		t.Fatalf("get candidate actions: %v", err)
	}
	defer actionsResponse.Body.Close()

	if actionsResponse.StatusCode != http.StatusOK {
		t.Fatalf("actions status = %d, want %d", actionsResponse.StatusCode, http.StatusOK)
	}

	var actionsPayload struct {
		Actions []map[string]any `json:"actions"`
	}
	if err := json.NewDecoder(actionsResponse.Body).Decode(&actionsPayload); err != nil {
		t.Fatalf("decode actions response: %v", err)
	}

	if len(actionsPayload.Actions) == 0 {
		t.Fatalf("expected at least one candidate action")
	}

	if actionType, ok := actionsPayload.Actions[0]["ActionType"].(string); !ok || actionType == "" {
		t.Fatalf("candidate action type missing from response")
	}

	if status, ok := actionsPayload.Actions[0]["Status"].(string); !ok || status != "awaiting_approval" {
		t.Fatalf("candidate action status = %v, want %q", actionsPayload.Actions[0]["Status"], "awaiting_approval")
	}

	uiResponse, err := http.Get(api.URL + "/ui/incidents/" + incidentID)
	if err != nil {
		t.Fatalf("get ui detail: %v", err)
	}
	defer uiResponse.Body.Close()

	uiBody, err := io.ReadAll(uiResponse.Body)
	if err != nil {
		t.Fatalf("read ui body: %v", err)
	}

	if !strings.Contains(string(uiBody), "checkout timeout after deploy") {
		t.Fatalf("ui body does not contain incident title")
	}

	if !strings.Contains(string(uiBody), "Candidate Actions") {
		t.Fatalf("ui body does not contain candidate actions section")
	}
}

func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent directories: %v", err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write file %s: %v", path, err)
	}
}
