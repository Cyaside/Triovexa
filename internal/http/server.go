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
			Phase:             "phase-01-read-only-triage",
			AvailableEndpoints: []string{
				"GET /health",
				"GET /debug/tools",
				"POST /webhooks/grafana",
				"GET /incidents",
				"GET /incidents/{id}",
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

		writeJSON(w, http.StatusOK, map[string]any{
			"incident":     record,
			"audit_events": auditEvents,
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
