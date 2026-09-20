package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/approval"
	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/execution"
	"github.com/Cyaside/Triovexa/internal/storage"
)

type incidentDetailResponse struct {
	Incident            domain.Incident             `json:"incident"`
	Triage              *domain.TriageResult        `json:"triage"`
	Evidence            []domain.EvidenceItem       `json:"evidence"`
	Documents           []domain.DocumentReference  `json:"documents"`
	CandidateActions    []domain.CandidateAction    `json:"candidate_actions"`
	PolicyDecisions     []domain.PolicyDecision     `json:"policy_decisions"`
	ApprovalRecords     []domain.ApprovalRecord     `json:"approval_records"`
	ExecutionRecords    []domain.ExecutionRecord    `json:"execution_records"`
	VerificationResults []domain.VerificationResult `json:"verification_results"`
	RollbackRecords     []domain.RollbackRecord     `json:"rollback_records"`
	AuditEvents         []domain.AuditEvent         `json:"audit_events"`
}

func registerAPIV1(
	mux *http.ServeMux,
	cfg config.Config,
	repository storage.Repository,
	approvalService *approval.Service,
	executionService *execution.Service,
	runtime *RuntimeControls,
) {
	mux.HandleFunc("/api/v1/incidents", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		items, err := repository.ListIncidents(r.Context())
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "storage_error", "failed to list incidents")
			return
		}
		query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
		status := strings.TrimSpace(r.URL.Query().Get("status"))
		severity := strings.TrimSpace(r.URL.Query().Get("severity"))
		service := strings.TrimSpace(r.URL.Query().Get("service"))
		environment := strings.TrimSpace(r.URL.Query().Get("environment"))
		filtered := make([]domain.Incident, 0, len(items))
		for _, item := range items {
			if query != "" && !strings.Contains(strings.ToLower(item.Title+" "+item.ServiceName+" "+item.ID), query) {
				continue
			}
			if status != "" && string(item.State) != status || severity != "" && item.Severity != severity || service != "" && item.ServiceName != service || environment != "" && item.Environment != environment {
				continue
			}
			filtered = append(filtered, item)
		}
		sort.Slice(filtered, func(i, j int) bool { return filtered[i].UpdatedAt.After(filtered[j].UpdatedAt) })
		page := positiveInt(r.URL.Query().Get("page"), 1)
		pageSize := positiveInt(r.URL.Query().Get("page_size"), 25)
		if pageSize > 100 {
			pageSize = 100
		}
		start := (page - 1) * pageSize
		if start > len(filtered) {
			start = len(filtered)
		}
		end := start + pageSize
		if end > len(filtered) {
			end = len(filtered)
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": filtered[start:end], "page": page, "page_size": pageSize, "total": len(filtered)})
	})

	mux.HandleFunc("/api/v1/incidents/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		incidentID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/incidents/"), "/")
		payload, err := loadIncidentDetail(r, repository, incidentID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				writeAPIError(w, http.StatusNotFound, "not_found", "incident not found")
				return
			}
			writeAPIError(w, http.StatusInternalServerError, "storage_error", "failed to load incident")
			return
		}
		writeJSON(w, http.StatusOK, payload)
	})

	mux.HandleFunc("/api/v1/approvals", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		incidents, err := repository.ListIncidents(r.Context())
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "storage_error", "failed to list approvals")
			return
		}
		type pendingApproval struct {
			Incident domain.Incident        `json:"incident"`
			Action   domain.CandidateAction `json:"action"`
		}
		pending := make([]pendingApproval, 0)
		for _, incident := range incidents {
			actions, listErr := repository.ListCandidateActions(r.Context(), incident.ID)
			if listErr != nil {
				continue
			}
			for _, action := range actions {
				if action.Status == domain.CandidateActionStatusAwaitingApproval {
					pending = append(pending, pendingApproval{Incident: incident, Action: action})
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": pending, "total": len(pending)})
	})

	mux.HandleFunc("/api/v1/actions/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/actions/"), "/")
		parts := strings.Split(path, "/")
		if len(parts) != 2 || parts[0] == "" {
			writeAPIError(w, http.StatusNotFound, "not_found", "action route not found")
			return
		}
		actor := actorFromRequest(r, "")
		var err error
		switch parts[1] {
		case "approve":
			_, err = approvalService.ApproveAction(r.Context(), parts[0], actor, "approved from operator console")
		case "reject":
			_, err = approvalService.RejectAction(r.Context(), parts[0], actor, "rejected from operator console")
		case "execute":
			_, err = executionService.ExecuteAction(r.Context(), parts[0], actor)
		default:
			writeAPIError(w, http.StatusNotFound, "not_found", "action route not found")
			return
		}
		if err != nil {
			statusCode := http.StatusBadRequest
			if strings.Contains(strings.ToLower(err.Error()), "already") || strings.Contains(strings.ToLower(err.Error()), "state") {
				statusCode = http.StatusConflict
			}
			writeAPIError(w, statusCode, "action_rejected", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "accepted"})
	})

	mux.HandleFunc("/api/v1/connections", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		provider := ProviderStatus{}
		if runtime != nil {
			provider = runtime.Providers
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"reasoning":    map[string]any{"configured": provider.LLMConfigured, "provider": provider.LLMProvider, "model": provider.LLMModel},
			"grafana":      map[string]any{"configured": provider.GrafanaConfigured, "metrics_source_uid": provider.MetricsSourceUID, "logs_source_uid": provider.LogsSourceUID},
			"prometheus":   serviceConnectionStatus(cfg.PrometheusBaseURL),
			"alertmanager": serviceConnectionStatus(cfg.AlertmanagerBaseURL),
		})
	})

	mux.HandleFunc("/api/v1/connections/reasoning/test", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		provider, baseURL, apiKey, model := cfg.EffectiveLLM()
		client, err := ai.NewOpenAICompatibleClient(ai.ProviderConfig{
			Name: provider, BaseURL: baseURL, APIKey: apiKey, Model: model,
			JSONMode: cfg.LLMJSONMode, Timeout: cfg.LLMTimeout,
			AllowHTTP: !cfg.InternalMode(), AllowHosts: cfg.LLMAllowHosts, RequireAllowlist: cfg.InternalMode(),
		})
		if err != nil || !client.Configured() {
			writeAPIError(w, http.StatusBadRequest, "provider_not_configured", "Reasoning provider configuration is incomplete or invalid.")
			return
		}
		started := time.Now()
		testCtx, cancel := context.WithTimeout(r.Context(), cfg.LLMTimeout)
		defer cancel()
		_, err = client.CompleteJSON(testCtx, []ai.ChatMessage{{Role: "system", Content: "Return a JSON object with status set to ok."}, {Role: "user", Content: "Test this connection."}})
		if err != nil {
			writeAPIError(w, http.StatusBadGateway, "provider_test_failed", "The provider did not return a valid JSON response. Check the server logs and provider settings.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "connected", "provider": provider, "model": model, "latency_ms": time.Since(started).Milliseconds()})
	})

	registerReadinessConnectionTest(mux, "/api/v1/connections/prometheus/test", "Prometheus", cfg.PrometheusBaseURL)
	registerReadinessConnectionTest(mux, "/api/v1/connections/alertmanager/test", "Alertmanager", cfg.AlertmanagerBaseURL)

	mux.HandleFunc("/api/v1/settings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			if approvalService == nil {
				writeAPIError(w, http.StatusServiceUnavailable, "settings_unavailable", "Safety settings are unavailable.")
				return
			}
			var payload struct {
				Enabled bool `json:"enabled"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&payload); err != nil {
				writeAPIError(w, http.StatusBadRequest, "invalid_request", "Invalid settings payload.")
				return
			}
			state := approvalService.SetKillSwitch(payload.Enabled)
			writeJSON(w, http.StatusOK, map[string]any{"kill_switch_enabled": state.Enabled, "updated_at": state.UpdatedAt})
			return
		}
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		killSwitch := false
		if approvalService != nil {
			killSwitch = approvalService.KillSwitchState().Enabled
		}
		writeJSON(w, http.StatusOK, map[string]any{"deployment_mode": cfg.DeploymentMode, "environment": cfg.Environment, "kill_switch_enabled": killSwitch})
	})
}

func serviceConnectionStatus(baseURL string) map[string]any {
	endpoint := publicConnectionEndpoint(baseURL)
	return map[string]any{
		"configured": endpoint != "",
		"endpoint":   endpoint,
	}
}

func publicConnectionEndpoint(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/")
}

func registerReadinessConnectionTest(mux *http.ServeMux, path string, serviceName string, baseURL string) {
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		endpoint := publicConnectionEndpoint(baseURL)
		if endpoint == "" {
			writeAPIError(w, http.StatusBadRequest, "connection_not_configured", serviceName+" is not configured.")
			return
		}
		started := time.Now()
		testCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := probeReadiness(testCtx, baseURL); err != nil {
			writeAPIError(w, http.StatusBadGateway, "connection_test_failed", serviceName+" did not pass its readiness check. Check the server logs and endpoint configuration.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "connected", "service": strings.ToLower(serviceName), "endpoint": endpoint, "latency_ms": time.Since(started).Milliseconds()})
	})
}

func probeReadiness(ctx context.Context, baseURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Host == "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("invalid readiness endpoint")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/-/ready"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return err
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("readiness endpoint returned status %d", response.StatusCode)
	}
	return nil
}

func loadIncidentDetail(r *http.Request, repository storage.Repository, id string) (incidentDetailResponse, error) {
	incident, err := repository.GetIncident(r.Context(), id)
	if err != nil {
		return incidentDetailResponse{}, err
	}
	evidence, err := repository.ListEvidenceItems(r.Context(), id)
	if err != nil {
		return incidentDetailResponse{}, err
	}
	documents, err := repository.ListDocumentReferences(r.Context(), id)
	if err != nil {
		return incidentDetailResponse{}, err
	}
	actions, err := repository.ListCandidateActions(r.Context(), id)
	if err != nil {
		return incidentDetailResponse{}, err
	}
	policies, err := repository.ListPolicyDecisions(r.Context(), id)
	if err != nil {
		return incidentDetailResponse{}, err
	}
	approvals, err := repository.ListApprovalRecords(r.Context(), id)
	if err != nil {
		return incidentDetailResponse{}, err
	}
	executions, err := repository.ListExecutionRecords(r.Context(), id)
	if err != nil {
		return incidentDetailResponse{}, err
	}
	verifications, err := repository.ListVerificationResults(r.Context(), id)
	if err != nil {
		return incidentDetailResponse{}, err
	}
	rollbacks, err := repository.ListRollbackRecords(r.Context(), id)
	if err != nil {
		return incidentDetailResponse{}, err
	}
	audit, err := repository.ListAuditEvents(r.Context(), id)
	if err != nil {
		return incidentDetailResponse{}, err
	}
	if evidence == nil {
		evidence = []domain.EvidenceItem{}
	}
	if documents == nil {
		documents = []domain.DocumentReference{}
	}
	if actions == nil {
		actions = []domain.CandidateAction{}
	}
	if policies == nil {
		policies = []domain.PolicyDecision{}
	}
	if approvals == nil {
		approvals = []domain.ApprovalRecord{}
	}
	if executions == nil {
		executions = []domain.ExecutionRecord{}
	}
	if verifications == nil {
		verifications = []domain.VerificationResult{}
	}
	if rollbacks == nil {
		rollbacks = []domain.RollbackRecord{}
	}
	if audit == nil {
		audit = []domain.AuditEvent{}
	}
	var triage *domain.TriageResult
	if value, triageErr := repository.GetTriageResult(r.Context(), id); triageErr == nil {
		triage = &value
	} else if !errors.Is(triageErr, storage.ErrNotFound) {
		return incidentDetailResponse{}, triageErr
	}
	return incidentDetailResponse{incident, triage, evidence, documents, actions, policies, approvals, executions, verifications, rollbacks, audit}, nil
}

func positiveInt(raw string, fallback int) int {
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return fallback
	}
	return value
}
