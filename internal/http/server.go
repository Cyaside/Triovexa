package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Cyaside/Triovexa/internal/alerting"
	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/incident"
	"github.com/Cyaside/Triovexa/internal/storage"
)

type ServerInfo struct {
	Name               string   `json:"name"`
	Environment        string   `json:"environment"`
	KillSwitchEnabled  bool     `json:"kill_switch_enabled"`
	Phase              string   `json:"phase"`
	AvailableEndpoints []string `json:"available_endpoints"`
	Timestamp          string   `json:"timestamp"`
}

func NewServer(
	cfg config.Config,
	logger *slog.Logger,
	repository storage.Repository,
	incidentService *incident.Service,
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
		writeJSON(w, http.StatusOK, ServerInfo{
			Name:              cfg.ServiceName,
			Environment:       cfg.Environment,
			KillSwitchEnabled: cfg.KillSwitchEnabled,
			Phase:             "phase-02-candidate-action-generation",
			AvailableEndpoints: []string{
				"GET /health",
				"GET /debug/tools",
				"POST /webhooks/grafana",
				"GET /incidents",
				"GET /incidents/{id}",
				"GET /incidents/{id}/triage",
				"GET /incidents/{id}/actions",
				"GET /ui/incidents",
				"GET /ui/incidents/{id}",
			},
			Timestamp: time.Now().UTC().Format(time.RFC3339),
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
			"incident":          record,
			"triage":            triageResult,
			"evidence":          evidence,
			"documents":         documents,
			"candidate_actions": actions,
			"audit_events":      auditEvents,
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

		renderIncidentList(w, incidentListPageData{Incidents: incidents})
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
			Incident:   record,
			Triage:     triageResult,
			Evidence:   evidence,
			Documents:  documents,
			Actions:    actions,
			AuditTrail: auditEvents,
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
