package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/approval"
	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/agent"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/storage"
)

func registerRepairAPI(mux *http.ServeMux, cfg config.Config, repository storage.Repository, runtime *RuntimeControls, approvalService *approval.Service) {
	store, _ := repository.(*storage.PostgresStore)
	mutationAllowed := func(w http.ResponseWriter) bool {
		if store == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "repair_unavailable", "Code repair requires PostgreSQL.")
			return false
		}
		killSwitch := cfg.KillSwitchEnabled
		if approvalService != nil {
			killSwitch = approvalService.KillSwitchState().Enabled
		}
		if killSwitch {
			writeAPIError(w, http.StatusForbidden, "kill_switch_enabled", "New repair work is disabled.")
			return false
		}
		return true
	}
	mux.HandleFunc("/api/v1/repair/bindings", func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "repair_unavailable", "Code repair requires PostgreSQL.")
			return
		}
		switch r.Method {
		case http.MethodGet:
			binding, err := store.GetActiveRepositoryBinding(r.Context(), r.URL.Query().Get("service"), r.URL.Query().Get("environment"))
			if errors.Is(err, storage.ErrNotFound) {
				writeAPIError(w, http.StatusNotFound, "not_found", "No repository binding exists for this target.")
				return
			}
			if err != nil {
				writeAPIError(w, http.StatusInternalServerError, "storage_error", "Failed to load repository binding.")
				return
			}
			writeJSON(w, http.StatusOK, binding)
		case http.MethodPost:
			if !mutationAllowed(w) {
				return
			}
			identity, ok := currentIdentity(r.Context())
			if !ok || identity.User.Role != domain.RoleAdmin {
				writeAPIError(w, http.StatusForbidden, "admin_required", "Administrator role is required.")
				return
			}
			var body struct {
				ServiceName   string   `json:"service_name"`
				Environment   string   `json:"environment"`
				RepositoryURL string   `json:"repository_url"`
				BaseRef       string   `json:"base_ref"`
				AllowedPaths  []string `json:"allowed_paths"`
				TestRecipes   []string `json:"test_recipes"`
			}
			if err := decodeBoundedJSON(w, r, &body); err != nil {
				writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
				return
			}
			now := time.Now().UTC()
			binding := coderepair.RepositoryBinding{ID: uuid.NewString(), ServiceName: body.ServiceName,
				Environment: body.Environment, RepositoryURL: body.RepositoryURL, BaseRef: body.BaseRef,
				AllowedPaths: body.AllowedPaths, TestRecipes: body.TestRecipes,
				PolicyVersion: "repair-v1", Enabled: true, CreatedAt: now, UpdatedAt: now}
			if err := binding.Validate(); err != nil {
				writeAPIError(w, http.StatusBadRequest, "invalid_binding", err.Error())
				return
			}
			if err := store.CreateRepositoryBinding(r.Context(), binding); err != nil {
				writeAPIError(w, http.StatusConflict, "binding_conflict", "A binding already exists for this target or could not be saved.")
				return
			}
			writeJSON(w, http.StatusCreated, binding)
		default:
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed.")
		}
	})
	mux.HandleFunc("/api/v1/repair/incidents/", func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "repair_unavailable", "Code repair requires PostgreSQL.")
			return
		}
		incidentID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/repair/incidents/"), "/")
		if incidentID == "" || strings.Contains(incidentID, "/") {
			writeAPIError(w, http.StatusNotFound, "not_found", "Incident route not found.")
			return
		}
		switch r.Method {
		case http.MethodGet:
			cases, err := store.ListRepairCasesForIncident(r.Context(), incidentID)
			if err != nil {
				writeAPIError(w, http.StatusInternalServerError, "storage_error", "Failed to load repair cases.")
				return
			}
			if cases == nil {
				cases = []coderepair.Case{}
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": cases})
		case http.MethodPost:
			if !mutationAllowed(w) {
				return
			}
			identity, ok := currentIdentity(r.Context())
			if !ok {
				writeAPIError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
				return
			}
			service, err := coderepair.NewProposalService(store, sandbox.ResolveBaseRevision, coderepair.EvidenceLimits{
				MaxItems: 40, MaxSnippetBytes: 2048, MaxTotalBytes: 32 * 1024, MaxAge: time.Minute,
			})
			if err != nil {
				writeAPIError(w, http.StatusInternalServerError, "repair_unavailable", "Repair proposal service is unavailable.")
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
			defer cancel()
			c, _, err := service.Propose(ctx, incidentID, identity.User.ID, time.Now().UTC())
			if err != nil {
				writeAPIError(w, http.StatusConflict, "repair_ineligible", err.Error())
				return
			}
			writeJSON(w, http.StatusCreated, c)
		default:
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed.")
		}
	})
	mux.HandleFunc("/api/v1/repair/cases/", func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "repair_unavailable", "Code repair requires PostgreSQL.")
			return
		}
		path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/repair/cases/"), "/")
		parts := strings.Split(path, "/")
		if len(parts) == 0 || len(parts) > 2 || parts[0] == "" {
			writeAPIError(w, http.StatusNotFound, "not_found", "Repair case route not found.")
			return
		}
		caseID := parts[0]
		if len(parts) == 1 && r.Method == http.MethodGet {
			writeRepairCaseDetail(w, r, store, caseID)
			return
		}
		if len(parts) != 2 || r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed.")
			return
		}
		if !mutationAllowed(w) {
			return
		}
		identity, ok := currentIdentity(r.Context())
		if !ok {
			writeAPIError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
			return
		}
		switch parts[1] {
		case "investigate":
			if runtime == nil || runtime.Reasoning == nil || !runtime.Reasoning.Configured() {
				writeAPIError(w, http.StatusServiceUnavailable, "reasoning_unavailable", "Configure a reasoning provider first.")
				return
			}
			service, err := coderepair.NewInvestigationAuthorizationService(store)
			if err != nil {
				writeAPIError(w, http.StatusInternalServerError, "repair_unavailable", "Investigation authorization is unavailable.")
				return
			}
			_, attempt, _, err := service.Authorize(r.Context(), caseID, identity.User.ID, coderepair.AgentSelection{
				Provider: runtime.Reasoning.Provider(), Model: runtime.Reasoning.Model(), PromptVersion: agent.PromptVersion,
			}, time.Now().UTC())
			if err != nil {
				writeAPIError(w, http.StatusConflict, "investigation_rejected", err.Error())
				return
			}
			writeJSON(w, http.StatusAccepted, map[string]any{"attempt_id": attempt.ID, "state": coderepair.StateInvestigating})
		case "review":
			var body struct {
				ExpectedVersion int64 `json:"expected_version"`
			}
			if err := decodeBoundedJSON(w, r, &body); err != nil {
				writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
				return
			}
			digest, updated, err := store.PrepareRepairPublication(r.Context(), caseID, identity.User.ID, body.ExpectedVersion, time.Now().UTC())
			if err != nil {
				writeAPIError(w, http.StatusConflict, "review_rejected", err.Error())
				return
			}
			if !updated {
				writeAPIError(w, http.StatusConflict, "state_changed", "Repair case changed. Refresh before reviewing.")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"review_digest": digest})
		case "publish":
			var body struct {
				ExpectedVersion int64  `json:"expected_version"`
				ReviewDigest    string `json:"review_digest"`
			}
			if err := decodeBoundedJSON(w, r, &body); err != nil {
				writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
				return
			}
			p, approved, err := store.ApproveRepairPublication(r.Context(), caseID, identity.User.ID, body.ReviewDigest, body.ExpectedVersion, time.Now().UTC())
			if err != nil {
				writeAPIError(w, http.StatusConflict, "publication_rejected", err.Error())
				return
			}
			if !approved {
				writeAPIError(w, http.StatusConflict, "state_changed", "Repair case changed. Refresh before publishing.")
				return
			}
			writeJSON(w, http.StatusAccepted, map[string]any{"operation_id": p.OperationID, "state": p.State})
		default:
			writeAPIError(w, http.StatusNotFound, "not_found", "Repair case route not found.")
		}
	})
}

func writeRepairCaseDetail(w http.ResponseWriter, r *http.Request, store *storage.PostgresStore, caseID string) {
	c, err := store.GetRepairCase(r.Context(), caseID)
	if errors.Is(err, storage.ErrNotFound) {
		writeAPIError(w, http.StatusNotFound, "not_found", "Repair case not found.")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_error", "Failed to load repair case.")
		return
	}
	binding, err := store.GetRepositoryBinding(r.Context(), c.BindingID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_error", "Failed to load repository binding.")
		return
	}
	snapshot, err := store.GetRepairEvidenceSnapshot(r.Context(), c.ID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_error", "Failed to load repair evidence.")
		return
	}
	events, err := store.ListRepairEvents(r.Context(), c.ID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_error", "Failed to load repair activity.")
		return
	}
	deployments, err := store.ListRepairDeployments(r.Context(), c.ID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_error", "Failed to load deployments.")
		return
	}
	response := map[string]any{"case": c, "binding": binding, "evidence": snapshot, "events": events, "deployments": deployments}
	if attempt, err := store.GetLatestRepairAttempt(r.Context(), c.ID); err == nil {
		response["attempt"] = attempt
		if report, err := store.GetRepairArtifactContent(r.Context(), attempt.ID, "investigation_report"); err == nil {
			response["report"] = json.RawMessage(report)
		}
		identity, ok := currentIdentity(r.Context())
		if ok && identity.User.Role != domain.RoleViewer {
			if patch, err := store.GetRepairArtifactContent(r.Context(), attempt.ID, "patch"); err == nil {
				response["patch"] = string(patch)
				digest := sha256.Sum256(patch)
				if reviewed, err := coderepair.PublicationDigest(c, attempt, hex.EncodeToString(digest[:])); err == nil {
					response["review_digest"] = reviewed
				}
			}
		}
	} else if !errors.Is(err, storage.ErrNotFound) {
		writeAPIError(w, http.StatusInternalServerError, "storage_error", "Failed to load attempt.")
		return
	}
	if p, err := store.GetRepairPublication(r.Context(), c.ID); err == nil {
		response["publication"] = p
	} else if !errors.Is(err, storage.ErrNotFound) {
		writeAPIError(w, http.StatusInternalServerError, "storage_error", "Failed to load publication.")
		return
	}
	if a, err := store.GetLatestRepairApproval(r.Context(), c.ID, "publication"); err == nil {
		response["publish_approval"] = a
	}
	writeJSON(w, http.StatusOK, response)
}
