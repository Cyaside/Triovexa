package http

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/storage"
)

func registerIncidentReadRoutes(mux *http.ServeMux, logger *slog.Logger, repository storage.Repository) {
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
}
