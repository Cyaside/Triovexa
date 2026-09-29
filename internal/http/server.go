package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Cyaside/Triovexa/internal/approval"
	"github.com/Cyaside/Triovexa/internal/auth"
	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/execution"
	"github.com/Cyaside/Triovexa/internal/incident"
	"github.com/Cyaside/Triovexa/internal/mode"
	"github.com/Cyaside/Triovexa/internal/storage"
	"github.com/Cyaside/Triovexa/internal/telemetry"
)

type ServerInfo struct {
	Name                string   `json:"name"`
	Environment         string   `json:"environment"`
	KillSwitchEnabled   bool     `json:"kill_switch_enabled"`
	KillSwitchUpdatedAt string   `json:"kill_switch_updated_at"`
	Phase               string   `json:"phase"`
	AvailableEndpoints  []string `json:"available_endpoints"`
	Timestamp           string   `json:"timestamp"`
}

func NewServerWithTelemetry(
	cfg config.Config,
	logger *slog.Logger,
	repository storage.Repository,
	incidentService *incident.Service,
	approvalService *approval.Service,
	executionService *execution.Service,
	recorder *telemetry.Recorder,
	runtimeControls ...*RuntimeControls,
) *http.Server {
	mux := http.NewServeMux()
	serverMetrics := recorder
	if serverMetrics == nil {
		serverMetrics = telemetry.NewRecorder()
	}
	var runtimeControl *RuntimeControls
	if len(runtimeControls) > 0 {
		runtimeControl = runtimeControls[0]
	}
	var authService = (*auth.Service)(nil)
	if runtimeControl != nil {
		authService = runtimeControl.Auth
	}
	registerSessionRoutes(mux, cfg, authService)
	registerAPIV1(mux, cfg, repository, approvalService, executionService, runtimeControl)
	registerRepairRoutes(mux, cfg, repository, runtimeControl, approvalService)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		http.Redirect(w, r, "/ui/incidents", http.StatusTemporaryRedirect)
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if runtimeControl != nil && runtimeControl.Readiness != nil {
			report := runtimeControl.Readiness.Check(r.Context())
			status := http.StatusOK
			if report.Status != "ok" {
				status = http.StatusServiceUnavailable
			}
			writeJSON(w, status, report)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"status":      "ok",
			"service":     cfg.ServiceName,
			"environment": cfg.Environment,
		})
	})

	mux.Handle("/metrics", serverMetrics)
	mux.Handle("/ui/assets/", uiAssetHandler)
	mux.HandleFunc("/ui/", serveUIApp)

	registerOperationalAdminRoutes(mux, cfg, approvalService, runtimeControl)
	registerGrafanaWebhook(mux, logger, incidentService)
	registerActionRoutes(mux, logger, repository, approvalService, executionService)
	registerIncidentReadRoutes(mux, logger, repository)

	return &http.Server{
		Addr:         cfg.HTTPAddress(),
		Handler:      withLogging(logger, serverMetrics, securityMiddleware(cfg, authService, serverMetrics, mux)),
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}
}

func withLogging(logger *slog.Logger, recorder *telemetry.Recorder, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		responseWriter := &statusCapturingResponseWriter{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
		}
		next.ServeHTTP(responseWriter, r)
		route := routeLabel(r.URL.Path)
		if recorder != nil {
			recorder.ObserveHTTPRequest(r.Method, route, responseWriter.statusCode, time.Since(startedAt))
		}
		logger.Info("http request completed",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("route", route),
			slog.Int("status", responseWriter.statusCode),
			slog.Duration("duration", time.Since(startedAt)),
		)
	})
}

type statusCapturingResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *statusCapturingResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *statusCapturingResponseWriter) Write(body []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func parseApprovalRequest(r *http.Request) (approvedBy string, note string, wantsHTML bool, err error) {
	contentType := r.Header.Get("Content-Type")
	wantsHTML = strings.Contains(contentType, "application/x-www-form-urlencoded")

	if wantsHTML {
		if err := r.ParseForm(); err != nil {
			return "", "", wantsHTML, errors.New("invalid approval form payload")
		}
		approvedBy = strings.TrimSpace(r.FormValue("approved_by"))
		note = strings.TrimSpace(r.FormValue("note"))
	} else {
		var payload struct {
			ApprovedBy string `json:"approved_by"`
			Note       string `json:"note"`
		}
		if r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				return "", "", wantsHTML, errors.New("invalid approval payload")
			}
		}
		approvedBy = strings.TrimSpace(payload.ApprovedBy)
		note = strings.TrimSpace(payload.Note)
	}

	if approvedBy == "" {
		approvedBy = strings.TrimSpace(r.Header.Get("X-Operator-Name"))
	}
	approvedBy = actorFromRequest(r, approvedBy)
	if approvedBy == "" {
		return "", "", wantsHTML, errors.New("approved_by is required")
	}

	return approvedBy, note, wantsHTML, nil
}

func parseKillSwitchRequest(r *http.Request) (bool, error) {
	if strings.Contains(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		if err := r.ParseForm(); err != nil {
			return false, errors.New("invalid kill switch form payload")
		}
		enabled := strings.EqualFold(strings.TrimSpace(r.FormValue("enabled")), "true")
		return enabled, nil
	}

	var payload struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		return false, errors.New("invalid kill switch payload")
	}

	return payload.Enabled, nil
}

func parseExecutionRequest(r *http.Request) (initiatedBy string, note string, wantsHTML bool, err error) {
	contentType := r.Header.Get("Content-Type")
	wantsHTML = strings.Contains(contentType, "application/x-www-form-urlencoded")

	if wantsHTML {
		if err := r.ParseForm(); err != nil {
			return "", "", wantsHTML, errors.New("invalid execution form payload")
		}
		initiatedBy = strings.TrimSpace(r.FormValue("initiated_by"))
		if initiatedBy == "" {
			initiatedBy = strings.TrimSpace(r.FormValue("approved_by"))
		}
		note = strings.TrimSpace(r.FormValue("note"))
	} else {
		var payload struct {
			InitiatedBy string `json:"initiated_by"`
			ApprovedBy  string `json:"approved_by"`
			Note        string `json:"note"`
		}
		if r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				return "", "", wantsHTML, errors.New("invalid execution payload")
			}
		}
		initiatedBy = strings.TrimSpace(payload.InitiatedBy)
		if initiatedBy == "" {
			initiatedBy = strings.TrimSpace(payload.ApprovedBy)
		}
		note = strings.TrimSpace(payload.Note)
	}

	if initiatedBy == "" {
		initiatedBy = strings.TrimSpace(r.Header.Get("X-Operator-Name"))
	}
	initiatedBy = actorFromRequest(r, initiatedBy)
	if initiatedBy == "" {
		return "", "", wantsHTML, errors.New("initiated_by is required")
	}

	return initiatedBy, note, wantsHTML, nil
}

func parseRuntimeModeRequest(r *http.Request) (reasoningMode string, observabilityMode string, err error) {
	if strings.Contains(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		reasoningMode = strings.TrimSpace(r.FormValue("reasoning_mode"))
		observabilityMode = strings.TrimSpace(r.FormValue("observability_mode"))
	} else {
		var payload struct {
			ReasoningMode     string `json:"reasoning_mode"`
			ObservabilityMode string `json:"observability_mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			return "", "", errors.New("invalid runtime mode payload")
		}
		reasoningMode = strings.TrimSpace(payload.ReasoningMode)
		observabilityMode = strings.TrimSpace(payload.ObservabilityMode)
	}

	if reasoningMode == "" && observabilityMode == "" {
		return "", "", errors.New("reasoning_mode or observability_mode is required")
	}
	if reasoningMode != "" {
		if _, err := mode.ParseReasoning(reasoningMode); err != nil {
			return "", "", err
		}
	}
	if observabilityMode != "" {
		if _, err := mode.ParseObservability(observabilityMode); err != nil {
			return "", "", err
		}
	}

	return reasoningMode, observabilityMode, nil
}

func routeLabel(path string) string {
	switch {
	case path == "/":
		return "/"
	case path == "/health":
		return "/health"
	case path == "/metrics":
		return "/metrics"
	case path == "/debug/tools":
		return "/debug/tools"
	case path == "/debug/policies":
		return "/debug/policies"
	case path == "/webhooks/grafana":
		return "/webhooks/grafana"
	case path == "/incidents":
		return "/incidents"
	case strings.HasPrefix(path, "/incidents/") && strings.HasSuffix(path, "/triage"):
		return "/incidents/{id}/triage"
	case strings.HasPrefix(path, "/incidents/") && strings.HasSuffix(path, "/actions"):
		return "/incidents/{id}/actions"
	case strings.HasPrefix(path, "/incidents/"):
		return "/incidents/{id}"
	case strings.HasPrefix(path, "/actions/") && strings.HasSuffix(path, "/verification"):
		return "/actions/{id}/verification"
	case strings.HasPrefix(path, "/actions/") && strings.HasSuffix(path, "/rollbacks"):
		return "/actions/{id}/rollbacks"
	case strings.HasPrefix(path, "/actions/") && strings.HasSuffix(path, "/approve"):
		return "/actions/{id}/approve"
	case strings.HasPrefix(path, "/actions/") && strings.HasSuffix(path, "/reject"):
		return "/actions/{id}/reject"
	case strings.HasPrefix(path, "/actions/") && strings.HasSuffix(path, "/execute"):
		return "/actions/{id}/execute"
	case path == "/admin/kill-switch":
		return "/admin/kill-switch"
	case path == "/admin/runtime-modes":
		return "/admin/runtime-modes"
	case path == "/ui/incidents":
		return "/ui/incidents"
	case path == "/ui/setup/observability":
		return "/ui/setup/observability"
	case path == "/ui/setup/observability/save-profile":
		return "/ui/setup/observability/save-profile"
	case path == "/ui/setup/observability/clear-profile":
		return "/ui/setup/observability/clear-profile"
	case path == "/ui/assets/workbench.css":
		return "/ui/assets/workbench.css"
	case path == "/ui/admin/kill-switch":
		return "/ui/admin/kill-switch"
	case path == "/ui/admin/runtime-modes":
		return "/ui/admin/runtime-modes"
	case path == "/ui/setup/observability/test-connection":
		return "/ui/setup/observability/test-connection"
	case path == "/ui/setup/observability/test-query":
		return "/ui/setup/observability/test-query"
	case strings.HasPrefix(path, "/ui/demo/scenarios/"):
		return "/ui/demo/scenarios/{scenario}"
	case strings.HasPrefix(path, "/ui/incidents/"):
		return "/ui/incidents/{id}"
	default:
		return path
	}
}

func catalogView(catalog execution.Catalog) []map[string]any {
	keys := make([]string, 0, len(catalog))
	for key := range catalog {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	views := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		definition := catalog[key]
		views = append(views, map[string]any{
			"key":                    definition.Key,
			"description":            definition.Description,
			"risk_level":             definition.RiskLevel,
			"approval_required":      definition.ApprovalRequired,
			"executable":             definition.Executable,
			"supports_rollback":      definition.SupportsRollback,
			"rollback_action_key":    definition.RollbackActionKey,
			"allowed_environments":   definition.AllowedEnvironments,
			"allowed_targets":        definition.AllowedTargets,
			"max_execution_attempts": definition.MaxExecutionAttempts,
			"execution_cooldown":     definition.ExecutionCooldown.String(),
			"parameters":             definition.Parameters,
		})
	}

	return views
}
