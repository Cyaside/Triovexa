package http

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
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
	"github.com/Cyaside/Triovexa/internal/telemetry"
	"github.com/Cyaside/Triovexa/internal/triage"
	"github.com/Cyaside/Triovexa/internal/verification"
)

func TestServerActionEndpointsReturnNotFoundForMissingCandidateAction(t *testing.T) {
	t.Parallel()

	repository := storage.NewMemoryStore()
	catalog := execution.DefaultCatalog()
	killSwitch := approval.NewKillSwitch(false)
	policyService := approval.NewService(repository, policy.NewEvaluator(catalog), killSwitch)
	executionService := execution.NewService(repository, catalog, &fakeHTTPAdapter{}, killSwitch, nil, 2*time.Second, 0, time.Minute)

	server := NewServer(config.Config{
		ServiceName:     "triovexa",
		Environment:     "test",
		HTTPPort:        "0",
		DatabaseURL:     "postgres://test",
		ReadTimeout:     5 * time.Second,
		WriteTimeout:    5 * time.Second,
		IdleTimeout:     5 * time.Second,
		ShutdownTimeout: 5 * time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), repository, nil, policyService, executionService)

	api := httptest.NewServer(server.Handler)
	defer api.Close()

	for _, endpoint := range []string{"/approve", "/reject", "/execute"} {
		body, err := json.Marshal(map[string]string{
			"approved_by":  "operator-x",
			"initiated_by": "operator-x",
		})
		if err != nil {
			t.Fatalf("marshal action payload: %v", err)
		}

		request, err := http.NewRequest(http.MethodPost, api.URL+"/actions/missing-action"+endpoint, bytes.NewReader(body))
		if err != nil {
			t.Fatalf("create request for %s: %v", endpoint, err)
		}
		request.Header.Set("Content-Type", "application/json")

		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("do request for %s: %v", endpoint, err)
		}
		response.Body.Close()

		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("%s status = %d, want %d", endpoint, response.StatusCode, http.StatusNotFound)
		}
	}
}

func TestServerMetricsEndpointExposesWorkflowMetrics(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	docsRoot := filepath.Join(tempDir, "docs")
	mustWriteFile(t, filepath.Join(docsRoot, "runbooks", "refresh-demo-cache.md"), "# Refresh demo cache\n\nRefresh cache after timeout spike.")
	mustWriteFile(t, filepath.Join(docsRoot, "postmortems", "checkout-timeout-after-deploy.md"), "# Checkout timeout after deploy\n\nTimeout observed after deploy.")

	var demoStateMu sync.Mutex
	demoState := demo.Snapshot{
		Mode:           demo.ModeTimeoutAfterDeploy,
		ErrorRate:      0.27,
		LatencyMs:      1200,
		QueueBacklog:   40,
		WorkerHealthy:  true,
		LastDeploy:     "v1.2.3",
		LastUpdatedUTC: time.Now().UTC(),
	}

	demoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		demoStateMu.Lock()
		defer demoStateMu.Unlock()

		switch r.URL.Path {
		case "/state":
			_ = json.NewEncoder(w).Encode(demoState)
		case "/actions/refresh-cache":
			demoState.Mode = demo.ModeHealthy
			demoState.ErrorRate = 0.02
			demoState.LatencyMs = 150
			demoState.QueueBacklog = 4
			demoState.WorkerHealthy = true
			demoState.LastUpdatedUTC = time.Now().UTC()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"action":  "refresh_demo_cache",
				"applied": true,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer demoServer.Close()

	repository := storage.NewMemoryStore()
	recorder := telemetry.NewRecorder()
	collector := observability.NewDemoCollector(demoServer.URL)
	retriever := retrieval.NewFileRetriever(docsRoot)
	catalog := execution.DefaultCatalog()
	generator := triage.NewHeuristicGenerator()
	actionGenerator := remediation.NewHeuristicGenerator(catalog)
	killSwitch := approval.NewKillSwitch(false)
	policyService := approval.NewService(repository, policy.NewEvaluator(catalog), killSwitch).WithTelemetry(recorder)
	rollbackService := execution.NewRollbackService(repository, catalog, execution.NewDemoAdapter(demoServer.URL))
	verificationService := verification.NewService(repository, verification.NewDemoSnapshotFetcher(demoServer.URL), catalog, rollbackService).WithTelemetry(recorder)
	executionService := execution.NewService(repository, catalog, execution.NewDemoAdapter(demoServer.URL), killSwitch, verificationService, 2*time.Second, 1, time.Minute).WithTelemetry(recorder)
	incidentService := incident.NewService(repository, collector, retriever, generator, actionGenerator, policyService).WithTelemetry(recorder)

	server := NewServerWithTelemetry(config.Config{
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
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), repository, incidentService, policyService, executionService, recorder)

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
				"fingerprint": "alert-metrics-001",
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

	var webhookPayload map[string]any
	if err := json.NewDecoder(webhookResponse.Body).Decode(&webhookPayload); err != nil {
		t.Fatalf("decode webhook response: %v", err)
	}

	waitForCandidateActionsReady(t, api.URL, webhookPayload["incident_id"].(string), 1)

	actionResponse, err := http.Get(api.URL + "/incidents/" + webhookPayload["incident_id"].(string) + "/actions")
	if err != nil {
		t.Fatalf("get actions: %v", err)
	}
	defer actionResponse.Body.Close()

	var actionsPayload struct {
		Actions []map[string]any `json:"actions"`
	}
	if err := json.NewDecoder(actionResponse.Body).Decode(&actionsPayload); err != nil {
		t.Fatalf("decode actions response: %v", err)
	}
	actionID := actionsPayload.Actions[0]["ID"].(string)

	approveBody, _ := json.Marshal(map[string]string{"approved_by": "operator-a"})
	approveRequest, err := http.NewRequest(http.MethodPost, api.URL+"/actions/"+actionID+"/approve", bytes.NewReader(approveBody))
	if err != nil {
		t.Fatalf("create approve request: %v", err)
	}
	approveRequest.Header.Set("Content-Type", "application/json")
	approveResponse, err := http.DefaultClient.Do(approveRequest)
	if err != nil {
		t.Fatalf("approve action: %v", err)
	}
	approveResponse.Body.Close()

	executeBody, _ := json.Marshal(map[string]string{"initiated_by": "operator-a"})
	executeRequest, err := http.NewRequest(http.MethodPost, api.URL+"/actions/"+actionID+"/execute", bytes.NewReader(executeBody))
	if err != nil {
		t.Fatalf("create execute request: %v", err)
	}
	executeRequest.Header.Set("Content-Type", "application/json")
	executeResponse, err := http.DefaultClient.Do(executeRequest)
	if err != nil {
		t.Fatalf("execute action: %v", err)
	}
	executeResponse.Body.Close()

	metricsResponse, err := http.Get(api.URL + "/metrics")
	if err != nil {
		t.Fatalf("get metrics: %v", err)
	}
	defer metricsResponse.Body.Close()

	metricsBody, err := io.ReadAll(metricsResponse.Body)
	if err != nil {
		t.Fatalf("read metrics body: %v", err)
	}

	metricsText := string(metricsBody)
	for _, expectedLine := range []string{
		`triovexa_incident_intake_total 1`,
		`triovexa_policy_decisions_total{decision="approval_required"} 1`,
		`triovexa_execution_outcomes_total{status="succeeded"} 1`,
		`triovexa_verification_outcomes_total{status="success"} 1`,
		`triovexa_triage_stage_duration_seconds_count{stage="action_generation",status="completed"} 1`,
		`triovexa_http_requests_total{method="POST",route="/webhooks/grafana",status="202"} 1`,
	} {
		if !strings.Contains(metricsText, expectedLine) {
			t.Fatalf("metrics body missing %q\n%s", expectedLine, metricsText)
		}
	}
}
