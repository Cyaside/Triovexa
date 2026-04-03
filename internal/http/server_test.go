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
	"sync"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/approval"
	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/demo"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/execution"
	"github.com/Cyaside/Triovexa/internal/incident"
	"github.com/Cyaside/Triovexa/internal/mode"
	"github.com/Cyaside/Triovexa/internal/observability"
	"github.com/Cyaside/Triovexa/internal/policy"
	"github.com/Cyaside/Triovexa/internal/remediation"
	"github.com/Cyaside/Triovexa/internal/retrieval"
	"github.com/Cyaside/Triovexa/internal/storage"
	"github.com/Cyaside/Triovexa/internal/telemetry"
	"github.com/Cyaside/Triovexa/internal/triage"
	"github.com/Cyaside/Triovexa/internal/verification"
)

func TestServerEndToEndReadOnlyTriage(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	docsRoot := filepath.Join(tempDir, "docs")
	mustWriteFile(t, filepath.Join(docsRoot, "runbooks", "restart-demo-worker.md"), "# Restart demo worker\n\nWorker backlog handling.")
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

	collector := observability.NewDemoCollector(demoServer.URL)
	retriever := retrieval.NewFileRetriever(docsRoot)
	catalog := execution.DefaultCatalog()
	generator := triage.NewHeuristicGenerator()
	actionGenerator := remediation.NewHeuristicGenerator(catalog)
	killSwitch := approval.NewKillSwitch(false)
	policyService := approval.NewService(repository, policy.NewEvaluator(catalog), killSwitch)
	rollbackService := execution.NewRollbackService(repository, catalog, execution.NewDemoAdapter(demoServer.URL))
	verificationService := verification.NewService(repository, verification.NewDemoSnapshotFetcher(demoServer.URL), catalog, rollbackService)
	executionService := execution.NewService(repository, catalog, execution.NewDemoAdapter(demoServer.URL), killSwitch, verificationService, 2*time.Second, 1, time.Minute)
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
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), repository, incidentService, policyService, executionService)

	api := httptest.NewServer(server.Handler)
	defer api.Close()

	listResponse, err := http.Get(api.URL + "/ui/incidents")
	if err != nil {
		t.Fatalf("get incident workbench list: %v", err)
	}
	defer listResponse.Body.Close()

	listBody, err := io.ReadAll(listResponse.Body)
	if err != nil {
		t.Fatalf("read list body: %v", err)
	}

	if !strings.Contains(string(listBody), "Demo Scenarios") {
		t.Fatalf("list body does not contain demo scenario section")
	}
	if !strings.Contains(string(listBody), "Trigger Scenario") {
		t.Fatalf("list body does not contain scenario trigger control")
	}

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
	if !ok || state != "triaging" {
		t.Fatalf("webhook state = %v, want %q", webhookPayload["state"], "triaging")
	}

	waitForTriageResult(t, api.URL, incidentID)
	waitForCandidateActionsReady(t, api.URL, incidentID, 1)

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

	if !strings.Contains(string(uiBody), "Action Lane") {
		t.Fatalf("ui body does not contain action lane section")
	}

	actionID, ok := actionsPayload.Actions[0]["ID"].(string)
	if !ok || actionID == "" {
		t.Fatalf("candidate action id missing from response")
	}

	approveBody, err := json.Marshal(map[string]string{
		"approved_by": "operator-a",
		"note":        "safe to proceed",
	})
	if err != nil {
		t.Fatalf("marshal approve payload: %v", err)
	}

	approveRequest, err := http.NewRequest(http.MethodPost, api.URL+"/actions/"+actionID+"/approve", bytes.NewReader(approveBody))
	if err != nil {
		t.Fatalf("create approve request: %v", err)
	}
	approveRequest.Header.Set("Content-Type", "application/json")

	approveResponse, err := http.DefaultClient.Do(approveRequest)
	if err != nil {
		t.Fatalf("approve action: %v", err)
	}
	defer approveResponse.Body.Close()

	if approveResponse.StatusCode != http.StatusOK {
		t.Fatalf("approve status = %d, want %d", approveResponse.StatusCode, http.StatusOK)
	}

	executeBody, err := json.Marshal(map[string]string{
		"initiated_by": "operator-a",
		"note":         "execute low-risk action",
	})
	if err != nil {
		t.Fatalf("marshal execute payload: %v", err)
	}

	executeRequest, err := http.NewRequest(http.MethodPost, api.URL+"/actions/"+actionID+"/execute", bytes.NewReader(executeBody))
	if err != nil {
		t.Fatalf("create execute request: %v", err)
	}
	executeRequest.Header.Set("Content-Type", "application/json")

	executeResponse, err := http.DefaultClient.Do(executeRequest)
	if err != nil {
		t.Fatalf("execute action: %v", err)
	}
	defer executeResponse.Body.Close()

	if executeResponse.StatusCode != http.StatusOK {
		t.Fatalf("execute status = %d, want %d", executeResponse.StatusCode, http.StatusOK)
	}

	actionsAfterApproveResponse, err := http.Get(api.URL + "/incidents/" + incidentID + "/actions")
	if err != nil {
		t.Fatalf("get candidate actions after approval: %v", err)
	}
	defer actionsAfterApproveResponse.Body.Close()

	var actionsAfterApprovePayload struct {
		Actions []map[string]any `json:"actions"`
	}
	if err := json.NewDecoder(actionsAfterApproveResponse.Body).Decode(&actionsAfterApprovePayload); err != nil {
		t.Fatalf("decode actions after approval response: %v", err)
	}

	if status, ok := actionsAfterApprovePayload.Actions[0]["Status"].(string); !ok || status != "succeeded" {
		t.Fatalf("candidate action status after execution = %v, want %q", actionsAfterApprovePayload.Actions[0]["Status"], "succeeded")
	}

	incidentResponse, err := http.Get(api.URL + "/incidents/" + incidentID)
	if err != nil {
		t.Fatalf("get incident detail: %v", err)
	}
	defer incidentResponse.Body.Close()

	var incidentPayload map[string]any
	if err := json.NewDecoder(incidentResponse.Body).Decode(&incidentPayload); err != nil {
		t.Fatalf("decode incident detail response: %v", err)
	}

	incidentValue, ok := incidentPayload["incident"].(map[string]any)
	if !ok {
		t.Fatalf("incident detail response is missing incident payload")
	}
	if state, ok := incidentValue["State"].(string); !ok || state != "resolved" {
		t.Fatalf("incident state after verification = %v, want %q", incidentValue["State"], "resolved")
	}

	verificationResponse, err := http.Get(api.URL + "/actions/" + actionID + "/verification")
	if err != nil {
		t.Fatalf("get verification results: %v", err)
	}
	defer verificationResponse.Body.Close()

	if verificationResponse.StatusCode != http.StatusOK {
		t.Fatalf("verification status = %d, want %d", verificationResponse.StatusCode, http.StatusOK)
	}

	var verificationPayload map[string]any
	if err := json.NewDecoder(verificationResponse.Body).Decode(&verificationPayload); err != nil {
		t.Fatalf("decode verification response: %v", err)
	}

	latestVerification, ok := verificationPayload["latest"].(map[string]any)
	if !ok {
		t.Fatalf("latest verification result is missing from response")
	}
	if status, ok := latestVerification["Status"].(string); !ok || status != "success" {
		t.Fatalf("verification latest status = %v, want %q", latestVerification["Status"], "success")
	}

	killSwitchBody, err := json.Marshal(map[string]bool{"enabled": true})
	if err != nil {
		t.Fatalf("marshal kill switch payload: %v", err)
	}

	killSwitchResponse, err := http.Post(api.URL+"/admin/kill-switch", "application/json", bytes.NewReader(killSwitchBody))
	if err != nil {
		t.Fatalf("toggle kill switch: %v", err)
	}
	defer killSwitchResponse.Body.Close()

	if killSwitchResponse.StatusCode != http.StatusOK {
		t.Fatalf("kill switch status = %d, want %d", killSwitchResponse.StatusCode, http.StatusOK)
	}

	debugResponse, err := http.Get(api.URL + "/debug/tools")
	if err != nil {
		t.Fatalf("get debug tools: %v", err)
	}
	defer debugResponse.Body.Close()

	var serverInfo map[string]any
	if err := json.NewDecoder(debugResponse.Body).Decode(&serverInfo); err != nil {
		t.Fatalf("decode debug tools response: %v", err)
	}

	if enabled, ok := serverInfo["kill_switch_enabled"].(bool); !ok || !enabled {
		t.Fatalf("kill switch should be enabled in debug response")
	}
}

func TestServerUIDemoScenarioTriggerRedirectsToIncidentDetail(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	docsRoot := filepath.Join(tempDir, "docs")
	mustWriteFile(t, filepath.Join(docsRoot, "runbooks", "refresh-demo-cache.md"), "# Refresh demo cache\n\nRefresh cache after timeout spike.")
	mustWriteFile(t, filepath.Join(docsRoot, "postmortems", "checkout-timeout-after-deploy.md"), "# Checkout timeout after deploy\n\nTimeout observed after deploy.")

	var demoStateMu sync.Mutex
	demoState := demo.Snapshot{
		Mode:           demo.ModeHealthy,
		ErrorRate:      0.01,
		LatencyMs:      120,
		QueueBacklog:   0,
		WorkerHealthy:  true,
		LastDeploy:     "v1.0.0",
		LastUpdatedUTC: time.Now().UTC(),
	}

	demoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		demoStateMu.Lock()
		defer demoStateMu.Unlock()

		switch r.URL.Path {
		case "/state":
			_ = json.NewEncoder(w).Encode(demoState)
		case "/simulate/timeout-after-deploy":
			demoState.Mode = demo.ModeTimeoutAfterDeploy
			demoState.ErrorRate = 0.27
			demoState.LatencyMs = 1250
			demoState.QueueBacklog = 46
			demoState.WorkerHealthy = true
			demoState.LastDeploy = "v1.1.0"
			demoState.LastUpdatedUTC = time.Now().UTC()
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(demoState)
		default:
			http.NotFound(w, r)
		}
	}))
	defer demoServer.Close()

	repository := storage.NewMemoryStore()
	collector := observability.NewDemoCollector(demoServer.URL)
	retriever := retrieval.NewFileRetriever(docsRoot)
	catalog := execution.DefaultCatalog()
	generator := triage.NewHeuristicGenerator()
	actionGenerator := remediation.NewHeuristicGenerator(catalog)
	killSwitch := approval.NewKillSwitch(false)
	policyService := approval.NewService(repository, policy.NewEvaluator(catalog), killSwitch)
	rollbackService := execution.NewRollbackService(repository, catalog, execution.NewDemoAdapter(demoServer.URL))
	verificationService := verification.NewService(repository, verification.NewDemoSnapshotFetcher(demoServer.URL), catalog, rollbackService)
	executionService := execution.NewService(repository, catalog, execution.NewDemoAdapter(demoServer.URL), killSwitch, verificationService, 2*time.Second, 1, time.Minute)
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
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), repository, incidentService, policyService, executionService)

	api := httptest.NewServer(server.Handler)
	defer api.Close()

	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	request, err := http.NewRequest(http.MethodPost, api.URL+"/ui/demo/scenarios/timeout-after-deploy", nil)
	if err != nil {
		t.Fatalf("create ui scenario request: %v", err)
	}

	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("post ui scenario: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("ui scenario status = %d, want %d", response.StatusCode, http.StatusSeeOther)
	}

	location := response.Header.Get("Location")
	if !strings.Contains(location, "/ui/incidents/") {
		t.Fatalf("expected redirect to incident detail, got %q", location)
	}
	if !strings.Contains(location, "notice=") {
		t.Fatalf("expected redirect to include notice query, got %q", location)
	}

	followResponse, err := http.Get(api.URL + location)
	if err != nil {
		t.Fatalf("follow redirect: %v", err)
	}
	defer followResponse.Body.Close()

	body, err := io.ReadAll(followResponse.Body)
	if err != nil {
		t.Fatalf("read redirected ui body: %v", err)
	}

	if !strings.Contains(string(body), "checkout timeout after deploy") {
		t.Fatalf("redirected ui body does not contain incident title")
	}
	if !strings.Contains(string(body), "Operator Step") {
		t.Fatalf("redirected ui body does not contain operator guidance")
	}
}

func TestServerUIServesWorkbenchStylesheet(t *testing.T) {
	t.Parallel()

	server := NewServer(config.Config{
		ServiceName:     "triovexa",
		Environment:     "test",
		HTTPPort:        "0",
		DatabaseURL:     "postgres://test",
		ReadTimeout:     5 * time.Second,
		WriteTimeout:    5 * time.Second,
		IdleTimeout:     5 * time.Second,
		ShutdownTimeout: 5 * time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), storage.NewMemoryStore(), nil, nil, nil)

	api := httptest.NewServer(server.Handler)
	defer api.Close()

	response, err := http.Get(api.URL + "/ui/assets/workbench.css")
	if err != nil {
		t.Fatalf("get workbench stylesheet: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("stylesheet status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read stylesheet body: %v", err)
	}

	if !strings.Contains(string(body), ".shell") {
		t.Fatalf("stylesheet body does not contain expected shell class")
	}
}

func TestServerEndToEndMediumRiskRollback(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	docsRoot := filepath.Join(tempDir, "docs")
	mustWriteFile(t, filepath.Join(docsRoot, "runbooks", "pause-demo-queue-consumer.md"), "# Pause demo queue consumer\n\nPause consumer while rollback plan is ready.")
	mustWriteFile(t, filepath.Join(docsRoot, "postmortems", "worker-stall.md"), "# Worker stall\n\nQueue consumers can be paused if backlog becomes dangerous.")

	var demoStateMu sync.Mutex
	demoState := demo.Snapshot{
		Mode:           demo.ModeWorkerStall,
		ErrorRate:      0.12,
		LatencyMs:      430,
		QueueBacklog:   128,
		ConsumerPaused: false,
		WorkerHealthy:  false,
		LastDeploy:     "v1.2.4",
		LastUpdatedUTC: time.Now().UTC(),
	}

	demoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		demoStateMu.Lock()
		defer demoStateMu.Unlock()

		switch r.URL.Path {
		case "/state":
			_ = json.NewEncoder(w).Encode(demoState)
		case "/actions/pause-queue-consumer":
			demoState.Mode = demo.ModeWorkerStall
			demoState.ErrorRate = 0.19
			demoState.LatencyMs = 710
			demoState.QueueBacklog = 160
			demoState.ConsumerPaused = true
			demoState.WorkerHealthy = false
			demoState.LastUpdatedUTC = time.Now().UTC()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"action":  "pause_demo_queue_consumer",
				"applied": true,
			})
		case "/actions/resume-queue-consumer":
			demoState.Mode = demo.ModeHealthy
			demoState.ErrorRate = 0.02
			demoState.LatencyMs = 180
			demoState.QueueBacklog = 10
			demoState.ConsumerPaused = false
			demoState.WorkerHealthy = true
			demoState.LastUpdatedUTC = time.Now().UTC()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"action":  "resume_demo_queue_consumer",
				"applied": true,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer demoServer.Close()

	repository := storage.NewMemoryStore()

	collector := observability.NewDemoCollector(demoServer.URL)
	retriever := retrieval.NewFileRetriever(docsRoot)
	catalog := execution.DefaultCatalog()
	generator := triage.NewHeuristicGenerator()
	actionGenerator := remediation.NewHeuristicGenerator(catalog)
	killSwitch := approval.NewKillSwitch(false)
	policyService := approval.NewService(repository, policy.NewEvaluator(catalog), killSwitch)
	rollbackService := execution.NewRollbackService(repository, catalog, execution.NewDemoAdapter(demoServer.URL))
	verificationService := verification.NewService(repository, verification.NewDemoSnapshotFetcher(demoServer.URL), catalog, rollbackService)
	executionService := execution.NewService(repository, catalog, execution.NewDemoAdapter(demoServer.URL), killSwitch, verificationService, 2*time.Second, 1, time.Minute)
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
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), repository, incidentService, policyService, executionService)

	api := httptest.NewServer(server.Handler)
	defer api.Close()

	payload := map[string]any{
		"title": "worker stall on checkout consumer",
		"commonLabels": map[string]string{
			"service":     "checkout-service",
			"environment": "staging",
			"severity":    "critical",
		},
		"alerts": []map[string]any{
			{
				"status":      "firing",
				"fingerprint": "alert-002",
				"startsAt":    time.Now().UTC().Format(time.RFC3339),
				"labels": map[string]string{
					"service":     "checkout-service",
					"environment": "staging",
					"severity":    "critical",
				},
				"annotations": map[string]string{
					"summary": "checkout worker stalled",
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

	incidentID, ok := webhookPayload["incident_id"].(string)
	if !ok || incidentID == "" {
		t.Fatalf("incident id missing from webhook response")
	}

	waitForCandidateActionsReady(t, api.URL, incidentID, 1)

	actionsResponse, err := http.Get(api.URL + "/incidents/" + incidentID + "/actions")
	if err != nil {
		t.Fatalf("get candidate actions: %v", err)
	}
	defer actionsResponse.Body.Close()

	var actionsPayload struct {
		Actions []map[string]any `json:"actions"`
	}
	if err := json.NewDecoder(actionsResponse.Body).Decode(&actionsPayload); err != nil {
		t.Fatalf("decode actions response: %v", err)
	}

	var mediumRiskActionID string
	for _, action := range actionsPayload.Actions {
		if actionType, _ := action["ActionType"].(string); actionType == "pause_demo_queue_consumer" {
			mediumRiskActionID, _ = action["ID"].(string)
			break
		}
	}
	if mediumRiskActionID == "" {
		t.Fatalf("expected medium-risk action pause_demo_queue_consumer to be present")
	}

	approveBody, err := json.Marshal(map[string]string{
		"approved_by": "operator-b",
		"note":        "pause consumer with rollback ready",
	})
	if err != nil {
		t.Fatalf("marshal approve payload: %v", err)
	}

	approveRequest, err := http.NewRequest(http.MethodPost, api.URL+"/actions/"+mediumRiskActionID+"/approve", bytes.NewReader(approveBody))
	if err != nil {
		t.Fatalf("create approve request: %v", err)
	}
	approveRequest.Header.Set("Content-Type", "application/json")

	approveResponse, err := http.DefaultClient.Do(approveRequest)
	if err != nil {
		t.Fatalf("approve action: %v", err)
	}
	defer approveResponse.Body.Close()

	executeBody, err := json.Marshal(map[string]string{
		"initiated_by": "operator-b",
		"note":         "execute medium-risk action",
	})
	if err != nil {
		t.Fatalf("marshal execute payload: %v", err)
	}

	executeRequest, err := http.NewRequest(http.MethodPost, api.URL+"/actions/"+mediumRiskActionID+"/execute", bytes.NewReader(executeBody))
	if err != nil {
		t.Fatalf("create execute request: %v", err)
	}
	executeRequest.Header.Set("Content-Type", "application/json")

	executeResponse, err := http.DefaultClient.Do(executeRequest)
	if err != nil {
		t.Fatalf("execute action: %v", err)
	}
	defer executeResponse.Body.Close()

	if executeResponse.StatusCode != http.StatusOK {
		t.Fatalf("execute status = %d, want %d", executeResponse.StatusCode, http.StatusOK)
	}

	incidentResponse, err := http.Get(api.URL + "/incidents/" + incidentID)
	if err != nil {
		t.Fatalf("get incident detail: %v", err)
	}
	defer incidentResponse.Body.Close()

	var incidentPayload map[string]any
	if err := json.NewDecoder(incidentResponse.Body).Decode(&incidentPayload); err != nil {
		t.Fatalf("decode incident detail: %v", err)
	}

	incidentValue, ok := incidentPayload["incident"].(map[string]any)
	if !ok {
		t.Fatalf("incident payload missing from detail response")
	}
	if state, ok := incidentValue["State"].(string); !ok || state != "rolled_back" {
		t.Fatalf("incident state = %v, want %q", incidentValue["State"], "rolled_back")
	}

	rollbackResponse, err := http.Get(api.URL + "/actions/" + mediumRiskActionID + "/rollbacks")
	if err != nil {
		t.Fatalf("get rollback records: %v", err)
	}
	defer rollbackResponse.Body.Close()

	var rollbackPayload map[string]any
	if err := json.NewDecoder(rollbackResponse.Body).Decode(&rollbackPayload); err != nil {
		t.Fatalf("decode rollback response: %v", err)
	}

	rollbackRecords, ok := rollbackPayload["rollback_records"].([]any)
	if !ok || len(rollbackRecords) != 1 {
		t.Fatalf("rollback records = %#v, want 1 record", rollbackPayload["rollback_records"])
	}
}

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

	if payload.Phase != "phase-07-hardening-portfolio-release" {
		t.Fatalf("phase = %q, want %q", payload.Phase, "phase-07-hardening-portfolio-release")
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
			MistralConfigured: true,
			GrafanaConfigured: true,
			MistralModel:      "mistral-small-latest",
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

	payload := strings.NewReader("reasoning_mode=mistral&observability_mode=grafana")
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
	if snapshot.Reasoning != mode.ReasoningMistral {
		t.Fatalf("reasoning mode = %q, want %q", snapshot.Reasoning, mode.ReasoningMistral)
	}
	if snapshot.Observability != mode.ObservabilityGrafana {
		t.Fatalf("observability mode = %q, want %q", snapshot.Observability, mode.ObservabilityGrafana)
	}
}

func TestServerIncidentWorkbenchShowsRuntimeControls(t *testing.T) {
	t.Parallel()

	repository := storage.NewMemoryStore()
	runtimeControls := &RuntimeControls{
		Modes: mode.NewManager("mistral", "grafana"),
		Providers: ProviderStatus{
			MistralConfigured: true,
			GrafanaConfigured: true,
			MistralModel:      "mistral-small-latest",
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

	text := string(body)
	for _, expected := range []string{
		"Switch Reasoning to heuristic",
		"Switch Observability to demo",
		"metrics UID grafanacloud-prom",
		"logs UID grafanacloud-logs",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("workbench body missing %q", expected)
		}
	}
}

func TestServerObservabilitySetupPageShowsLocalFirstGuidance(t *testing.T) {
	t.Parallel()

	repository := storage.NewMemoryStore()
	runtimeControls := &RuntimeControls{
		Modes: mode.NewManager("heuristic", "demo"),
		Providers: ProviderStatus{
			MistralConfigured: false,
			GrafanaConfigured: false,
		},
	}
	server := NewServerWithTelemetry(config.Config{
		ServiceName:             "triovexa",
		Environment:             "test",
		HTTPPort:                "0",
		DatabaseURL:             "memory",
		GrafanaMetricsSourceUID: "grafanacloud-prom",
		GrafanaLogsSourceUID:    "grafanacloud-logs",
		ReadTimeout:             5 * time.Second,
		WriteTimeout:            5 * time.Second,
		IdleTimeout:             5 * time.Second,
		ShutdownTimeout:         5 * time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), repository, nil, nil, nil, telemetry.NewRecorder(), runtimeControls)

	api := httptest.NewServer(server.Handler)
	defer api.Close()

	response, err := http.Get(api.URL + "/ui/setup/observability")
	if err != nil {
		t.Fatalf("get observability setup page: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("setup page status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read setup page body: %v", err)
	}

	text := string(body)
	for _, expected := range []string{
		"Grafana Setup Preview",
		"100% local, single-user workflow",
		"Run Query Preview",
		"Test Connection",
		"Save Local Profile",
		"No saved local profile yet",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("setup page body missing %q", expected)
		}
	}
}

func TestServerObservabilitySetupCanSaveAndClearLocalProfile(t *testing.T) {
	t.Parallel()

	profilePath := filepath.Join(t.TempDir(), "observability-profile.json")
	repository := storage.NewMemoryStore()
	runtimeControls := &RuntimeControls{
		Modes: mode.NewManager("heuristic", "demo"),
		Providers: ProviderStatus{
			MistralConfigured: false,
			GrafanaConfigured: false,
		},
	}
	server := NewServerWithTelemetry(config.Config{
		ServiceName:                   "triovexa",
		Environment:                   "test",
		HTTPPort:                      "0",
		DatabaseURL:                   "memory",
		LocalObservabilityProfilePath: profilePath,
		GrafanaMetricsSourceUID:       "grafanacloud-prom",
		GrafanaLogsSourceUID:          "grafanacloud-logs",
		ReadTimeout:                   5 * time.Second,
		WriteTimeout:                  5 * time.Second,
		IdleTimeout:                   5 * time.Second,
		ShutdownTimeout:               5 * time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), repository, nil, nil, nil, telemetry.NewRecorder(), runtimeControls)

	api := httptest.NewServer(server.Handler)
	defer api.Close()

	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	saveForm := strings.NewReader(strings.Join([]string{
		"grafana_base_url=" + urlQueryEscape("https://grafana.example.com"),
		"grafana_api_token=" + urlQueryEscape("local-token"),
		"grafana_metrics_datasource_uid=metrics-uid",
		"grafana_logs_datasource_uid=logs-uid",
		"grafana_error_rate_query=" + urlQueryEscape("vector(0.12)"),
		"sample_service=checkout-service",
	}, "&"))

	saveRequest, err := http.NewRequest(http.MethodPost, api.URL+"/ui/setup/observability/save-profile", saveForm)
	if err != nil {
		t.Fatalf("new save profile request: %v", err)
	}
	saveRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	saveResponse, err := client.Do(saveRequest)
	if err != nil {
		t.Fatalf("post save profile: %v", err)
	}
	defer saveResponse.Body.Close()

	if saveResponse.StatusCode != http.StatusSeeOther {
		t.Fatalf("save profile status = %d, want %d", saveResponse.StatusCode, http.StatusSeeOther)
	}

	savedProfile, ok, err := config.LoadObservabilityProfile(profilePath)
	if err != nil {
		t.Fatalf("LoadObservabilityProfile() error = %v", err)
	}
	if !ok {
		t.Fatalf("expected local observability profile to be saved")
	}
	if savedProfile.BaseURL != "https://grafana.example.com" {
		t.Fatalf("saved profile BaseURL = %q, want %q", savedProfile.BaseURL, "https://grafana.example.com")
	}
	if savedProfile.APIToken != "local-token" {
		t.Fatalf("saved profile APIToken = %q, want %q", savedProfile.APIToken, "local-token")
	}

	location := saveResponse.Header.Get("Location")
	if !strings.Contains(location, "notice=") {
		t.Fatalf("save redirect missing notice, got %q", location)
	}

	followSaveResponse, err := http.Get(api.URL + location)
	if err != nil {
		t.Fatalf("get saved setup page: %v", err)
	}
	defer followSaveResponse.Body.Close()

	followSaveBody, err := io.ReadAll(followSaveResponse.Body)
	if err != nil {
		t.Fatalf("read saved setup page body: %v", err)
	}

	followSaveText := string(followSaveBody)
	for _, expected := range []string{
		"Saved local profile found",
		"https://grafana.example.com",
		"Restart the server if you want runtime defaults to reload from the saved profile.",
	} {
		if !strings.Contains(followSaveText, expected) {
			t.Fatalf("saved setup page missing %q", expected)
		}
	}

	clearRequest, err := http.NewRequest(http.MethodPost, api.URL+"/ui/setup/observability/clear-profile", nil)
	if err != nil {
		t.Fatalf("new clear profile request: %v", err)
	}

	clearResponse, err := client.Do(clearRequest)
	if err != nil {
		t.Fatalf("post clear profile: %v", err)
	}
	defer clearResponse.Body.Close()

	if clearResponse.StatusCode != http.StatusSeeOther {
		t.Fatalf("clear profile status = %d, want %d", clearResponse.StatusCode, http.StatusSeeOther)
	}

	_, ok, err = config.LoadObservabilityProfile(profilePath)
	if err != nil {
		t.Fatalf("LoadObservabilityProfile() after clear error = %v", err)
	}
	if ok {
		t.Fatalf("expected local observability profile to be cleared")
	}
}

func TestServerObservabilitySetupQueryPreviewRendersDatasourceAndEvidence(t *testing.T) {
	t.Parallel()

	grafanaServer := newFakeGrafanaSetupServer(t)
	defer grafanaServer.Close()

	repository := storage.NewMemoryStore()
	runtimeControls := &RuntimeControls{
		Modes: mode.NewManager("heuristic", "demo"),
		Providers: ProviderStatus{
			MistralConfigured: false,
			GrafanaConfigured: false,
		},
	}
	server := NewServerWithTelemetry(config.Config{
		ServiceName:     "triovexa",
		Environment:     "test",
		HTTPPort:        "0",
		DatabaseURL:     "memory",
		ReadTimeout:     5 * time.Second,
		WriteTimeout:    5 * time.Second,
		IdleTimeout:     5 * time.Second,
		ShutdownTimeout: 5 * time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), repository, nil, nil, nil, telemetry.NewRecorder(), runtimeControls)

	api := httptest.NewServer(server.Handler)
	defer api.Close()

	form := strings.NewReader(strings.Join([]string{
		"grafana_base_url=" + urlQueryEscape(grafanaServer.URL),
		"grafana_api_token=test-token",
		"grafana_metrics_datasource_uid=test-prom",
		"grafana_logs_datasource_uid=test-logs",
		"grafana_error_rate_query=" + urlQueryEscape("vector(0.12)"),
		"grafana_latency_query=" + urlQueryEscape("vector(220)"),
		"grafana_queue_query=" + urlQueryEscape("vector(4)"),
		"grafana_replica_query=" + urlQueryEscape("vector(2)"),
		"grafana_logs_query=" + urlQueryEscape("{service=\"{{service}}\"}"),
		"grafana_deploy_logs_query=" + urlQueryEscape("{service=\"{{service}}\"} |= \"deploy\""),
		"sample_service=checkout-service",
		"sample_environment=staging",
		"sample_severity=critical",
		"sample_title=" + urlQueryEscape("checkout timeout after deploy"),
	}, "&"))

	request, err := http.NewRequest(http.MethodPost, api.URL+"/ui/setup/observability/test-query", form)
	if err != nil {
		t.Fatalf("new query preview request: %v", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("post query preview: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("query preview status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read query preview body: %v", err)
	}

	text := string(body)
	for _, expected := range []string{
		"Connected successfully. 2 datasource(s) discovered.",
		"Primary Metrics",
		"Primary Logs",
		"Error Rate",
		"0.1200",
		"Deploy Logs",
		"checkout-service deployment v1.2.3",
		"grafana-prometheus",
		"grafana-loki",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("query preview body missing %q", expected)
		}
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
