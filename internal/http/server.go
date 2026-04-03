package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Cyaside/Triovexa/internal/alerting"
	"github.com/Cyaside/Triovexa/internal/approval"
	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/domain"
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

func NewServer(
	cfg config.Config,
	logger *slog.Logger,
	repository storage.Repository,
	incidentService *incident.Service,
	approvalService *approval.Service,
	executionService *execution.Service,
	runtimeControls ...*RuntimeControls,
) *http.Server {
	return NewServerWithTelemetry(cfg, logger, repository, incidentService, approvalService, executionService, nil, runtimeControls...)
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

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		http.Redirect(w, r, "/ui/incidents", http.StatusTemporaryRedirect)
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":      "ok",
			"service":     cfg.ServiceName,
			"environment": cfg.Environment,
		})
	})

	mux.Handle("/metrics", serverMetrics)
	mux.Handle("/ui/assets/", uiAssetHandler)

	mux.HandleFunc("/debug/tools", func(w http.ResponseWriter, r *http.Request) {
		killSwitchState := approval.KillSwitchState{
			Enabled:   cfg.KillSwitchEnabled,
			UpdatedAt: time.Now().UTC(),
		}
		if approvalService != nil {
			killSwitchState = approvalService.KillSwitchState()
		}

		payload := map[string]any{
			"name":                   cfg.ServiceName,
			"environment":            cfg.Environment,
			"kill_switch_enabled":    killSwitchState.Enabled,
			"kill_switch_updated_at": killSwitchState.UpdatedAt.Format(time.RFC3339),
			"phase":                  "phase-07-hardening-portfolio-release",
			"available_endpoints": []string{
				"GET /health",
				"GET /metrics",
				"GET /debug/tools",
				"GET /debug/policies",
				"POST /webhooks/grafana",
				"GET /incidents",
				"GET /incidents/{id}",
				"GET /incidents/{id}/triage",
				"GET /incidents/{id}/actions",
				"GET /actions/{id}/verification",
				"GET /actions/{id}/rollbacks",
				"POST /actions/{id}/approve",
				"POST /actions/{id}/reject",
				"POST /actions/{id}/execute",
				"POST /admin/kill-switch",
				"POST /admin/runtime-modes",
				"GET /ui/assets/workbench.css",
				"GET /ui/incidents",
				"GET /ui/incidents/{id}",
				"GET /ui/setup/observability",
				"POST /ui/setup/observability/test-connection",
				"POST /ui/setup/observability/test-query",
				"POST /ui/setup/observability/save-profile",
				"POST /ui/setup/observability/clear-profile",
			},
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		}
		if runtimeControl != nil {
			payload["runtime"] = buildRuntimeViewData(r.Context(), runtimeControl)
		}

		writeJSON(w, http.StatusOK, payload)
	})

	mux.HandleFunc("/debug/policies", func(w http.ResponseWriter, r *http.Request) {
		killSwitchState := approval.KillSwitchState{
			Enabled:   cfg.KillSwitchEnabled,
			UpdatedAt: time.Now().UTC(),
		}
		if approvalService != nil {
			killSwitchState = approvalService.KillSwitchState()
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"phase":               "phase-07-hardening-portfolio-release",
			"kill_switch_enabled": killSwitchState.Enabled,
			"catalog":             catalogView(execution.DefaultCatalog()),
		})
	})

	mux.HandleFunc("/admin/kill-switch", func(w http.ResponseWriter, r *http.Request) {
		if approvalService == nil {
			writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "approval workflow is not configured"})
			return
		}

		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		enabled, err := parseKillSwitchRequest(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		state := approvalService.SetKillSwitch(enabled)
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled":    state.Enabled,
			"updated_at": state.UpdatedAt.Format(time.RFC3339),
		})
	})

	mux.HandleFunc("/admin/runtime-modes", func(w http.ResponseWriter, r *http.Request) {
		if runtimeControl == nil || runtimeControl.Modes == nil {
			writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "runtime mode controls are not configured"})
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		reasoningMode, observabilityMode, err := parseRuntimeModeRequest(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		snapshot := runtimeControl.Modes.Snapshot()
		if reasoningMode != "" {
			snapshot = runtimeControl.Modes.SetReasoning(reasoningMode)
		}
		if observabilityMode != "" {
			snapshot = runtimeControl.Modes.SetObservability(observabilityMode)
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"reasoning_mode":     snapshot.Reasoning,
			"observability_mode": snapshot.Observability,
		})
	})

	mux.HandleFunc("/webhooks/grafana", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		var payload alerting.GrafanaWebhookPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid grafana payload"})
			return
		}

		record, err := incidentService.IngestGrafanaWebhookAsync(r.Context(), payload)
		if err != nil {
			logger.Error("failed to ingest grafana webhook", slog.String("error", err.Error()))
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		writeJSON(w, http.StatusAccepted, map[string]any{
			"incident_id":       record.ID,
			"external_alert_id": record.ExternalAlertID,
			"state":             record.State,
			"title":             record.Title,
		})
	})

	mux.HandleFunc("/actions/", func(w http.ResponseWriter, r *http.Request) {
		if approvalService == nil && executionService == nil && r.Method != http.MethodGet {
			writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "action workflow is not configured"})
			return
		}

		actionPath := strings.TrimPrefix(r.URL.Path, "/actions/")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(actionPath, "/verification"):
			actionID := strings.TrimSuffix(actionPath, "/verification")
			if _, err := repository.GetCandidateAction(r.Context(), actionID); err != nil {
				if errors.Is(err, storage.ErrNotFound) {
					writeJSON(w, http.StatusNotFound, map[string]string{"error": "candidate action not found"})
					return
				}
				logger.Error("failed to load candidate action before verification lookup", slog.String("error", err.Error()))
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load candidate action"})
				return
			}

			results, err := repository.ListVerificationResultsByAction(r.Context(), actionID)
			if err != nil {
				logger.Error("failed to list verification results", slog.String("error", err.Error()))
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get verification results"})
				return
			}

			var latest *domain.VerificationResult
			if len(results) > 0 {
				copy := results[len(results)-1]
				latest = &copy
			}

			writeJSON(w, http.StatusOK, map[string]any{
				"action_id":            actionID,
				"verification_results": results,
				"latest":               latest,
			})
			return
		case r.Method == http.MethodGet && strings.HasSuffix(actionPath, "/rollbacks"):
			actionID := strings.TrimSuffix(actionPath, "/rollbacks")
			if _, err := repository.GetCandidateAction(r.Context(), actionID); err != nil {
				if errors.Is(err, storage.ErrNotFound) {
					writeJSON(w, http.StatusNotFound, map[string]string{"error": "candidate action not found"})
					return
				}
				logger.Error("failed to load candidate action before rollback lookup", slog.String("error", err.Error()))
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load candidate action"})
				return
			}

			results, err := repository.ListRollbackRecordsByAction(r.Context(), actionID)
			if err != nil {
				logger.Error("failed to list rollback records", slog.String("error", err.Error()))
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get rollback records"})
				return
			}

			writeJSON(w, http.StatusOK, map[string]any{
				"action_id":        actionID,
				"rollback_records": results,
			})
			return
		case r.Method != http.MethodPost:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		case strings.HasSuffix(actionPath, "/approve"):
			if approvalService == nil {
				writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "approval workflow is not configured"})
				return
			}
			actionID := strings.TrimSuffix(actionPath, "/approve")
			approvedBy, note, wantsHTML, err := parseApprovalRequest(r)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}

			action, err := approvalService.ApproveAction(r.Context(), actionID, approvedBy, note)
			if err != nil {
				logger.Error("failed to approve action", slog.String("error", err.Error()))
				if errors.Is(err, storage.ErrNotFound) {
					writeJSON(w, http.StatusNotFound, map[string]string{"error": "candidate action not found"})
					return
				}
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}

			if wantsHTML {
				http.Redirect(w, r, "/ui/incidents/"+action.IncidentID, http.StatusSeeOther)
				return
			}

			writeJSON(w, http.StatusOK, map[string]any{"action": action})
		case strings.HasSuffix(actionPath, "/reject"):
			if approvalService == nil {
				writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "approval workflow is not configured"})
				return
			}
			actionID := strings.TrimSuffix(actionPath, "/reject")
			approvedBy, note, wantsHTML, err := parseApprovalRequest(r)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}

			action, err := approvalService.RejectAction(r.Context(), actionID, approvedBy, note)
			if err != nil {
				logger.Error("failed to reject action", slog.String("error", err.Error()))
				if errors.Is(err, storage.ErrNotFound) {
					writeJSON(w, http.StatusNotFound, map[string]string{"error": "candidate action not found"})
					return
				}
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}

			if wantsHTML {
				http.Redirect(w, r, "/ui/incidents/"+action.IncidentID, http.StatusSeeOther)
				return
			}

			writeJSON(w, http.StatusOK, map[string]any{"action": action})
		case strings.HasSuffix(actionPath, "/execute"):
			if executionService == nil {
				writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "execution workflow is not configured"})
				return
			}
			actionID := strings.TrimSuffix(actionPath, "/execute")
			candidateAction, candidateErr := repository.GetCandidateAction(r.Context(), actionID)
			if candidateErr != nil {
				logger.Error("failed to load candidate action before execute", slog.String("error", candidateErr.Error()))
				if errors.Is(candidateErr, storage.ErrNotFound) {
					writeJSON(w, http.StatusNotFound, map[string]string{"error": "candidate action not found"})
					return
				}
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": candidateErr.Error()})
				return
			}

			initiatedBy, _, wantsHTML, err := parseExecutionRequest(r)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}

			record, err := executionService.ExecuteAction(r.Context(), actionID, initiatedBy)
			if err != nil {
				logger.Error("failed to execute action", slog.String("error", err.Error()))
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}

			if wantsHTML {
				http.Redirect(w, r, "/ui/incidents/"+candidateAction.IncidentID, http.StatusSeeOther)
				return
			}

			writeJSON(w, http.StatusOK, map[string]any{"execution": record})
		default:
			http.NotFound(w, r)
		}
	})

	mux.HandleFunc("/incidents", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		incidents, err := repository.ListIncidents(r.Context())
		if err != nil {
			logger.Error("failed to list incidents", slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list incidents"})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"incidents": incidents})
	})

	mux.HandleFunc("/incidents/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		incidentID := strings.TrimPrefix(r.URL.Path, "/incidents/")
		if incidentID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "incident id is required"})
			return
		}

		if strings.HasSuffix(incidentID, "/triage") {
			incidentID = strings.TrimSuffix(incidentID, "/triage")
			triageResult, err := repository.GetTriageResult(r.Context(), incidentID)
			if err != nil {
				if errors.Is(err, storage.ErrNotFound) {
					writeJSON(w, http.StatusNotFound, map[string]string{"error": "triage result not found"})
					return
				}

				logger.Error("failed to get triage result", slog.String("error", err.Error()))
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get triage result"})
				return
			}

			writeJSON(w, http.StatusOK, map[string]any{"triage": triageResult})
			return
		}

		if strings.HasSuffix(incidentID, "/actions") {
			incidentID = strings.TrimSuffix(incidentID, "/actions")
			actions, err := repository.ListCandidateActions(r.Context(), incidentID)
			if err != nil {
				logger.Error("failed to list candidate actions", slog.String("error", err.Error()))
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get candidate actions"})
				return
			}

			writeJSON(w, http.StatusOK, map[string]any{"actions": actions})
			return
		}

		record, err := repository.GetIncident(r.Context(), incidentID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "incident not found"})
				return
			}

			logger.Error("failed to get incident", slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get incident"})
			return
		}

		auditEvents, err := repository.ListAuditEvents(r.Context(), incidentID)
		if err != nil {
			logger.Error("failed to list audit events", slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get incident audit trail"})
			return
		}

		evidence, err := repository.ListEvidenceItems(r.Context(), incidentID)
		if err != nil {
			logger.Error("failed to list evidence", slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get incident evidence"})
			return
		}

		documents, err := repository.ListDocumentReferences(r.Context(), incidentID)
		if err != nil {
			logger.Error("failed to list related documents", slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get incident documents"})
			return
		}

		actions, err := repository.ListCandidateActions(r.Context(), incidentID)
		if err != nil {
			logger.Error("failed to list candidate actions", slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get candidate actions"})
			return
		}

		policyDecisions, err := repository.ListPolicyDecisions(r.Context(), incidentID)
		if err != nil {
			logger.Error("failed to list policy decisions", slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get policy decisions"})
			return
		}

		approvalRecords, err := repository.ListApprovalRecords(r.Context(), incidentID)
		if err != nil {
			logger.Error("failed to list approval records", slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get approval records"})
			return
		}

		executionRecords, err := repository.ListExecutionRecords(r.Context(), incidentID)
		if err != nil {
			logger.Error("failed to list execution records", slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get execution records"})
			return
		}

		verificationResults, err := repository.ListVerificationResults(r.Context(), incidentID)
		if err != nil {
			logger.Error("failed to list verification results", slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get verification results"})
			return
		}

		rollbackRecords, err := repository.ListRollbackRecords(r.Context(), incidentID)
		if err != nil {
			logger.Error("failed to list rollback records", slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get rollback records"})
			return
		}

		var triageResult *domain.TriageResult
		result, err := repository.GetTriageResult(r.Context(), incidentID)
		if err == nil {
			triageResult = &result
		} else if !errors.Is(err, storage.ErrNotFound) {
			logger.Error("failed to get triage result", slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get incident triage"})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"incident":             record,
			"triage":               triageResult,
			"evidence":             evidence,
			"documents":            documents,
			"candidate_actions":    actions,
			"policy_decisions":     policyDecisions,
			"approval_records":     approvalRecords,
			"execution_records":    executionRecords,
			"verification_results": verificationResults,
			"rollback_records":     rollbackRecords,
			"audit_events":         auditEvents,
		})
	})

	mux.HandleFunc("/ui/incidents", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		incidents, err := repository.ListIncidents(r.Context())
		if err != nil {
			http.Error(w, "failed to load incidents", http.StatusInternalServerError)
			return
		}

		items, err := buildIncidentListItems(r.Context(), repository, incidents)
		if err != nil {
			http.Error(w, "failed to build incident workbench", http.StatusInternalServerError)
			return
		}

		renderIncidentList(w, incidentListPageData{
			Items:             items,
			KillSwitchEnabled: approvalService != nil && approvalService.KillSwitchState().Enabled,
			Stats:             buildIncidentDashboardStats(incidents),
			DemoScenarios:     demoScenarioViews(),
			Runtime:           buildRuntimeViewData(r.Context(), runtimeControl),
			Notice:            strings.TrimSpace(r.URL.Query().Get("notice")),
			Error:             strings.TrimSpace(r.URL.Query().Get("error")),
		})
	})

	mux.HandleFunc("/ui/demo/scenarios/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if incidentService == nil {
			http.Error(w, "incident workflow is not configured", http.StatusNotImplemented)
			return
		}

		scenarioKey := strings.TrimPrefix(r.URL.Path, "/ui/demo/scenarios/")
		incidentRecord, err := triggerDemoScenario(r.Context(), cfg.DemoServiceBaseURL, incidentService, scenarioKey)
		if err != nil {
			logger.Error("failed to trigger demo scenario", slog.String("scenario", scenarioKey), slog.String("error", err.Error()))
			target := appendUIMessage("/ui/incidents", "error", "Gagal menjalankan demo scenario. Periksa log server untuk detail.")
			if errors.Is(err, errDemoScenarioNotFound) {
				target = appendUIMessage("/ui/incidents", "error", "Demo scenario tidak dikenal.")
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}

		target := appendUIMessage(
			"/ui/incidents/"+incidentRecord.ID,
			"notice",
			"Demo scenario berhasil dijalankan. Incident baru dan hasil heuristiknya sudah siap ditinjau.",
		)
		http.Redirect(w, r, target, http.StatusSeeOther)
	})

	mux.HandleFunc("/ui/admin/kill-switch", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if approvalService == nil {
			http.Error(w, "approval workflow is not configured", http.StatusNotImplemented)
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form payload", http.StatusBadRequest)
			return
		}

		enabled, err := parseKillSwitchRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		state := approvalService.SetKillSwitch(enabled)
		target := sanitizeUIRedirectTarget(r.FormValue("redirect"), "/ui/incidents")
		message := "Kill switch dinonaktifkan. Approval dan execution manual kembali bisa dipakai."
		if state.Enabled {
			message = "Kill switch diaktifkan. Flow triage tetap berjalan, tetapi action baru akan diblok."
		}
		http.Redirect(w, r, appendUIMessage(target, "notice", message), http.StatusSeeOther)
	})

	mux.HandleFunc("/ui/admin/runtime-modes", func(w http.ResponseWriter, r *http.Request) {
		if runtimeControl == nil || runtimeControl.Modes == nil {
			http.Error(w, "runtime mode controls are not configured", http.StatusNotImplemented)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form payload", http.StatusBadRequest)
			return
		}

		reasoningMode, observabilityMode, err := parseRuntimeModeRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		snapshot := runtimeControl.Modes.Snapshot()
		if reasoningMode != "" {
			snapshot = runtimeControl.Modes.SetReasoning(reasoningMode)
		}
		if observabilityMode != "" {
			snapshot = runtimeControl.Modes.SetObservability(observabilityMode)
		}

		target := sanitizeUIRedirectTarget(r.FormValue("redirect"), "/ui/incidents")
		message := fmt.Sprintf("Runtime mode diperbarui. Reasoning=%s, Observability=%s.", snapshot.Reasoning, snapshot.Observability)
		http.Redirect(w, r, appendUIMessage(target, "notice", message), http.StatusSeeOther)
	})

	loadSetupDefaults := func() (observabilitySetupForm, observabilityProfileState) {
		return loadObservabilitySetupDefaults(cfg)
	}
	mux.HandleFunc("/ui/setup/observability", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		setupForm, profileState := loadSetupDefaults()
		renderObservabilitySetupPage(w, observabilitySetupPageData{
			Form:          setupForm,
			Runtime:       buildRuntimeViewData(r.Context(), runtimeControl),
			LocalModeNote: buildLocalModeNote(),
			Profile:       profileState,
			Readiness:     buildObservabilityReadiness(setupForm, observabilityConnectionResult{}, nil),
			Notice:        strings.TrimSpace(r.URL.Query().Get("notice")),
			Error:         strings.TrimSpace(r.URL.Query().Get("error")),
		})
	})

	mux.HandleFunc("/ui/setup/observability/test-connection", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		setupForm, profileState := loadSetupDefaults()
		form, err := parseObservabilitySetupForm(r, setupForm)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		connection, datasources := runObservabilityConnectionTest(r.Context(), form)
		renderObservabilitySetupPage(w, observabilitySetupPageData{
			Form:          form,
			Runtime:       buildRuntimeViewData(r.Context(), runtimeControl),
			LocalModeNote: buildLocalModeNote(),
			Profile:       profileState,
			Connection:    connection,
			Datasources:   datasources,
			Readiness:     buildObservabilityReadiness(form, connection, nil),
		})
	})

	mux.HandleFunc("/ui/setup/observability/test-query", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		setupForm, profileState := loadSetupDefaults()
		form, err := parseObservabilitySetupForm(r, setupForm)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		connection, datasources, queryResults, evidence := runObservabilityQueryPreview(r.Context(), form)
		renderObservabilitySetupPage(w, observabilitySetupPageData{
			Form:          form,
			Runtime:       buildRuntimeViewData(r.Context(), runtimeControl),
			LocalModeNote: buildLocalModeNote(),
			Profile:       profileState,
			Connection:    connection,
			Datasources:   datasources,
			QueryResults:  queryResults,
			Evidence:      evidence,
			Readiness:     buildObservabilityReadiness(form, connection, queryResults),
		})
	})

	mux.HandleFunc("/ui/setup/observability/save-profile", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		setupForm, _ := loadSetupDefaults()
		form, err := parseObservabilitySetupForm(r, setupForm)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if err := config.SaveObservabilityProfile(cfg.LocalObservabilityProfilePath, profileFromObservabilitySetupForm(form)); err != nil {
			target := appendUIMessage("/ui/setup/observability", "error", "Failed to save the local observability profile.")
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}

		target := appendUIMessage(
			"/ui/setup/observability",
			"notice",
			"Local observability profile saved. Restart the server if you want runtime defaults to reload from the saved profile.",
		)
		http.Redirect(w, r, target, http.StatusSeeOther)
	})

	mux.HandleFunc("/ui/setup/observability/clear-profile", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if err := config.ClearObservabilityProfile(cfg.LocalObservabilityProfilePath); err != nil {
			target := appendUIMessage("/ui/setup/observability", "error", "Failed to clear the local observability profile.")
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}

		target := appendUIMessage("/ui/setup/observability", "notice", "Saved local observability profile cleared from this machine.")
		http.Redirect(w, r, target, http.StatusSeeOther)
	})

	mux.HandleFunc("/ui/incidents/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		incidentID := strings.TrimPrefix(r.URL.Path, "/ui/incidents/")
		if incidentID == "" {
			http.Error(w, "incident id is required", http.StatusBadRequest)
			return
		}

		record, err := repository.GetIncident(r.Context(), incidentID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, "failed to load incident", http.StatusInternalServerError)
			return
		}

		evidence, err := repository.ListEvidenceItems(r.Context(), incidentID)
		if err != nil {
			http.Error(w, "failed to load evidence", http.StatusInternalServerError)
			return
		}

		documents, err := repository.ListDocumentReferences(r.Context(), incidentID)
		if err != nil {
			http.Error(w, "failed to load documents", http.StatusInternalServerError)
			return
		}

		actions, err := repository.ListCandidateActions(r.Context(), incidentID)
		if err != nil {
			http.Error(w, "failed to load candidate actions", http.StatusInternalServerError)
			return
		}

		policyDecisions, err := repository.ListPolicyDecisions(r.Context(), incidentID)
		if err != nil {
			http.Error(w, "failed to load policy decisions", http.StatusInternalServerError)
			return
		}

		approvalRecords, err := repository.ListApprovalRecords(r.Context(), incidentID)
		if err != nil {
			http.Error(w, "failed to load approval records", http.StatusInternalServerError)
			return
		}

		executionRecords, err := repository.ListExecutionRecords(r.Context(), incidentID)
		if err != nil {
			http.Error(w, "failed to load execution records", http.StatusInternalServerError)
			return
		}

		verificationResults, err := repository.ListVerificationResults(r.Context(), incidentID)
		if err != nil {
			http.Error(w, "failed to load verification results", http.StatusInternalServerError)
			return
		}

		rollbackRecords, err := repository.ListRollbackRecords(r.Context(), incidentID)
		if err != nil {
			http.Error(w, "failed to load rollback records", http.StatusInternalServerError)
			return
		}

		auditEvents, err := repository.ListAuditEvents(r.Context(), incidentID)
		if err != nil {
			http.Error(w, "failed to load audit trail", http.StatusInternalServerError)
			return
		}

		var triageResult *domain.TriageResult
		result, err := repository.GetTriageResult(r.Context(), incidentID)
		if err == nil {
			triageResult = &result
		} else if !errors.Is(err, storage.ErrNotFound) {
			http.Error(w, "failed to load triage result", http.StatusInternalServerError)
			return
		}

		renderIncidentDetail(w, incidentDetailPageData{
			Incident:            record,
			Triage:              triageResult,
			Evidence:            evidence,
			Documents:           documents,
			Actions:             actions,
			PolicyDecisions:     policyDecisions,
			ApprovalRecords:     approvalRecords,
			ExecutionRecords:    executionRecords,
			VerificationResults: verificationResults,
			RollbackRecords:     rollbackRecords,
			KillSwitchEnabled:   approvalService != nil && approvalService.KillSwitchState().Enabled,
			AuditTrail:          auditEvents,
			Runtime:             buildRuntimeViewData(r.Context(), runtimeControl),
			Notice:              strings.TrimSpace(r.URL.Query().Get("notice")),
			Error:               strings.TrimSpace(r.URL.Query().Get("error")),
			NextOperatorStep:    describeNextOperatorStep(record, approvalService != nil && approvalService.KillSwitchState().Enabled),
		})
	})

	return &http.Server{
		Addr:         cfg.HTTPAddress(),
		Handler:      withLogging(logger, serverMetrics, mux),
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
