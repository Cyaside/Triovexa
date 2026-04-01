package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Cyaside/Triovexa/internal/alerting"
	"github.com/Cyaside/Triovexa/internal/approval"
	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/execution"
	"github.com/Cyaside/Triovexa/internal/incident"
	"github.com/Cyaside/Triovexa/internal/storage"
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
) *http.Server {
	mux := http.NewServeMux()

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

	mux.HandleFunc("/debug/tools", func(w http.ResponseWriter, r *http.Request) {
		killSwitchState := approval.KillSwitchState{
			Enabled:   cfg.KillSwitchEnabled,
			UpdatedAt: time.Now().UTC(),
		}
		if approvalService != nil {
			killSwitchState = approvalService.KillSwitchState()
		}

		writeJSON(w, http.StatusOK, ServerInfo{
			Name:                cfg.ServiceName,
			Environment:         cfg.Environment,
			KillSwitchEnabled:   killSwitchState.Enabled,
			KillSwitchUpdatedAt: killSwitchState.UpdatedAt.Format(time.RFC3339),
			Phase:               "phase-06-rollback-medium-risk-actions",
			AvailableEndpoints: []string{
				"GET /health",
				"GET /debug/tools",
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
				"GET /ui/incidents",
				"GET /ui/incidents/{id}",
			},
			Timestamp: time.Now().UTC().Format(time.RFC3339),
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

		record, err := incidentService.IngestGrafanaWebhook(r.Context(), payload)
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

		renderIncidentList(w, incidentListPageData{
			Incidents:         incidents,
			KillSwitchEnabled: approvalService != nil && approvalService.KillSwitchState().Enabled,
		})
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
		})
	})

	return &http.Server{
		Addr:         cfg.HTTPAddress(),
		Handler:      withLogging(logger, mux),
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}
}

func withLogging(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		next.ServeHTTP(w, r)
		logger.Info("http request completed",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Duration("duration", time.Since(startedAt)),
		)
	})
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
