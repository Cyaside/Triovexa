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

	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/demo"
	"github.com/Cyaside/Triovexa/internal/incident"
	"github.com/Cyaside/Triovexa/internal/observability"
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

	repository, err := storage.NewSQLiteStore(filepath.Join(tempDir, "triovexa-test.db"))
	if err != nil {
		t.Fatalf("create sqlite store: %v", err)
	}
	defer func() {
		_ = repository.Close()
	}()

	collector := observability.NewDemoCollector(demoServer.URL)
	retriever := retrieval.NewFileRetriever(docsRoot)
	generator := triage.NewHeuristicGenerator()
	incidentService := incident.NewService(repository, collector, retriever, generator)

	server := NewServer(config.Config{
		ServiceName:        "triovexa",
		Environment:        "test",
		HTTPPort:           "0",
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

	triageResponse, err := http.Get(api.URL + "/incidents/" + incidentID + "/triage")
	if err != nil {
		t.Fatalf("get triage response: %v", err)
	}
	defer triageResponse.Body.Close()

	if triageResponse.StatusCode != http.StatusOK {
		t.Fatalf("triage status = %d, want %d", triageResponse.StatusCode, http.StatusOK)
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
