package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/Cyaside/Triovexa/internal/alerting"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/incident"
)

func registerGrafanaWebhook(mux *http.ServeMux, logger *slog.Logger, incidentService *incident.Service) {
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

		results := make([]map[string]any, 0, len(payload.Alerts))
		var first domain.Incident
		var firstSet bool
		for _, grafanaAlert := range payload.Alerts {
			single := payload
			single.Alerts = []alerting.GrafanaAlert{grafanaAlert}
			record, ingestErr := incidentService.IngestGrafanaWebhookAsync(r.Context(), single)
			if ingestErr != nil {
				if errors.Is(ingestErr, incident.ErrResolvedAlertIgnored) {
					results = append(results, map[string]any{"ignored": true, "reason": ingestErr.Error(), "fingerprint": grafanaAlert.Fingerprint})
					continue
				}
				logger.Error("failed to ingest grafana webhook alert", slog.String("fingerprint", grafanaAlert.Fingerprint), slog.String("error", ingestErr.Error()))
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": ingestErr.Error()})
				return
			}
			if !firstSet {
				first, firstSet = record, true
			}
			results = append(results, map[string]any{"incident_id": record.ID, "external_alert_id": record.ExternalAlertID, "state": record.State, "title": record.Title})
		}
		if !firstSet {
			writeJSON(w, http.StatusAccepted, map[string]any{"ignored": true, "results": results})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"incident_id": first.ID, "external_alert_id": first.ExternalAlertID,
			"state": first.State, "title": first.Title, "results": results,
		})
	})
}
