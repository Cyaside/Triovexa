package http

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Cyaside/Triovexa/internal/approval"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/execution"
	"github.com/Cyaside/Triovexa/internal/storage"
)

func registerActionRoutes(mux *http.ServeMux, logger *slog.Logger, repository storage.Repository, approvalService *approval.Service, executionService *execution.Service) {
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

			approvedBy = actorFromRequest(r, approvedBy)
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

			approvedBy = actorFromRequest(r, approvedBy)
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

			initiatedBy = actorFromRequest(r, initiatedBy)
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
}
