package http

import (
	"net/http"
	"time"

	"github.com/Cyaside/Triovexa/internal/approval"
	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/execution"
)

func registerOperationalAdminRoutes(mux *http.ServeMux, cfg config.Config, approvalService *approval.Service, runtimeControl *RuntimeControls) {
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
			"phase":                  "phase-07-hardening-release",
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
				"GET /api/v1/incidents",
				"GET /api/v1/incidents/{id}",
				"GET /api/v1/approvals",
				"GET /api/v1/connections",
				"PUT /api/v1/connections/grafana/config",
				"PUT /api/v1/connections/reasoning/config",
				"GET /api/v1/playground",
				"POST /api/v1/playground/faults",
				"GET /ui/*",
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
			"phase":               "phase-07-hardening-release",
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
}
