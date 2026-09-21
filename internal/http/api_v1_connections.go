package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/observability"
	"github.com/Cyaside/Triovexa/internal/storage"
)

type reasoningConnectionRequest struct {
	config.ReasoningConnectionProfile
	APIKey string `json:"api_key"`
}

func registerConnectionConfigurationAPI(mux *http.ServeMux, cfg config.Config, repository storage.Repository, runtimeControls ...*RuntimeControls) {
	settings, _ := repository.(storage.SettingsStore)
	var runtime *RuntimeControls
	if len(runtimeControls) > 0 {
		runtime = runtimeControls[0]
	}

	mux.HandleFunc("/api/v1/connections/reasoning/config", func(w http.ResponseWriter, r *http.Request) {
		profile := effectiveReasoningProfile(r.Context(), cfg, settings)
		_, persisted := effectiveReasoningBundle(r.Context(), settings)
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, reasoningProfileResponse(profile, runtime, persisted))
		case http.MethodPut:
			if settings == nil {
				writeAPIError(w, http.StatusServiceUnavailable, "settings_unavailable", "Connection settings are unavailable.")
				return
			}
			requested := reasoningConnectionRequest{ReasoningConnectionProfile: profile}
			if err := decodeBoundedJSON(w, r, &requested); err != nil {
				writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
				return
			}
			profile = requested.ReasoningConnectionProfile
			providedAPIKey := strings.TrimSpace(requested.APIKey)
			apiKey := providedAPIKey
			bundle, hasBundle := effectiveReasoningBundle(r.Context(), settings)
			if providedAPIKey != "" {
				if runtime == nil || runtime.Secrets == nil {
					writeAPIError(w, http.StatusServiceUnavailable, "credential_storage_unavailable", "Encrypted credential storage is unavailable.")
					return
				}
				profile.CredentialRef = "ENCRYPTED_LLM_API_KEY"
			}
			raw, err := config.EncodeConnectionProfile(profile)
			if err == nil {
				profile, err = config.DecodeReasoningConnectionProfile(raw)
			}
			if err != nil {
				writeAPIError(w, http.StatusBadRequest, "invalid_connection_profile", err.Error())
				return
			}
			if apiKey == "" {
				apiKey, _ = config.ResolveCredential(profile.CredentialRef)
			}
			if apiKey == "" && (runtime == nil || runtime.Reasoning == nil || !runtime.Reasoning.Configured()) {
				writeAPIError(w, http.StatusBadRequest, "credential_unavailable", "Enter an API key to activate this provider.")
				return
			}
			validationKey := apiKey
			if validationKey == "" {
				validationKey = "retained-runtime-credential"
			}
			if candidate, candidateErr := ai.NewOpenAICompatibleClient(reasoningProviderConfig(cfg, profile, validationKey)); candidateErr != nil || !candidate.Configured() {
				writeAPIError(w, http.StatusBadRequest, "provider_not_configured", "Reasoning provider configuration is incomplete or invalid.")
				return
			}
			if providedAPIKey != "" {
				encrypted, encryptErr := runtime.Secrets.Encrypt(apiKey)
				if encryptErr != nil {
					writeAPIError(w, http.StatusInternalServerError, "credential_storage_error", "Failed to encrypt the provider credential.")
					return
				}
				bundle = config.ReasoningConnectionBundle{Profile: profile, EncryptedAPIKey: encrypted, EncryptionVersion: 1}
				hasBundle = true
			} else if hasBundle {
				bundle.Profile = profile
			}
			settingKey := config.ReasoningConnectionSettingKey
			settingValue := raw
			if hasBundle {
				settingKey = config.ReasoningConnectionBundleSettingKey
				settingValue, err = config.EncodeConnectionProfile(bundle)
				if err != nil {
					writeAPIError(w, http.StatusInternalServerError, "credential_storage_error", "Failed to encode the provider credential.")
					return
				}
			}
			if err := settings.PutSetting(r.Context(), settingKey, settingValue); err != nil {
				writeAPIError(w, http.StatusInternalServerError, "settings_error", "Failed to save the reasoning connection profile.")
				return
			}
			if runtime != nil && runtime.Reasoning != nil {
				if err := runtime.Reasoning.Reconfigure(reasoningProviderConfig(cfg, profile, apiKey)); err != nil {
					writeAPIError(w, http.StatusInternalServerError, "provider_activation_failed", "The saved provider could not be activated.")
					return
				}
			}
			if runtime != nil && runtime.Modes != nil && runtime.Reasoning != nil && runtime.Reasoning.Configured() {
				runtime.Modes.SetReasoning("llm")
			}
			payload := reasoningProfileResponse(profile, runtime, hasBundle)
			payload["restart_required"] = false
			writeJSON(w, http.StatusOK, payload)
		default:
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
	})

	mux.HandleFunc("/api/v1/connections/grafana/config", func(w http.ResponseWriter, r *http.Request) {
		profile := effectiveGrafanaProfile(r.Context(), cfg, settings)
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, grafanaProfileResponse(profile))
		case http.MethodPut:
			if settings == nil {
				writeAPIError(w, http.StatusServiceUnavailable, "settings_unavailable", "Connection settings are unavailable.")
				return
			}
			var requested config.GrafanaConnectionProfile
			if err := decodeBoundedJSON(w, r, &requested); err != nil {
				writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
				return
			}
			raw, err := config.EncodeConnectionProfile(requested)
			if err == nil {
				requested, err = config.DecodeGrafanaConnectionProfile(raw)
			}
			if err != nil {
				writeAPIError(w, http.StatusBadRequest, "invalid_connection_profile", err.Error())
				return
			}
			if err := settings.PutSetting(r.Context(), config.GrafanaConnectionSettingKey, raw); err != nil {
				writeAPIError(w, http.StatusInternalServerError, "settings_error", "Failed to save the Grafana connection profile.")
				return
			}
			payload := grafanaProfileResponse(requested)
			payload["restart_required"] = true
			writeJSON(w, http.StatusOK, payload)
		default:
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
	})

	mux.HandleFunc("/api/v1/connections/grafana/test", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		profile := effectiveGrafanaProfile(r.Context(), cfg, settings)
		if r.ContentLength != 0 {
			if err := decodeBoundedJSON(w, r, &profile); err != nil {
				writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
				return
			}
		}
		if err := config.ValidateGrafanaConnectionProfile(profile); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_connection_profile", err.Error())
			return
		}
		credential, configured := config.ResolveCredential(profile.CredentialRef)
		if !configured {
			writeAPIError(w, http.StatusBadRequest, "credential_unavailable", "The credential reference is not available in the server environment.")
			return
		}
		client := observability.NewGrafanaClient(profile.BaseURL, credential, profile.MetricsSourceUID, profile.LogsSourceUID)
		started := time.Now()
		sources, err := client.ListDatasources(r.Context())
		if err != nil {
			writeAPIError(w, http.StatusBadGateway, "connection_test_failed", "Grafana did not accept the connection. Check the endpoint, credential reference, and server logs.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "connected", "latency_ms": time.Since(started).Milliseconds(), "datasources": sources})
	})

	mux.HandleFunc("/api/v1/connections/grafana/preview", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		profile := effectiveGrafanaProfile(r.Context(), cfg, settings)
		if err := decodeBoundedJSON(w, r, &profile); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if err := config.ValidateGrafanaConnectionProfile(profile); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_connection_profile", err.Error())
			return
		}
		credential, configured := config.ResolveCredential(profile.CredentialRef)
		if !configured {
			writeAPIError(w, http.StatusBadRequest, "credential_unavailable", "The credential reference is not available in the server environment.")
			return
		}
		client := observability.NewGrafanaClient(profile.BaseURL, credential, profile.MetricsSourceUID, profile.LogsSourceUID)
		collector := observability.NewGrafanaCollector(client, observability.GrafanaSignalConfig{
			ErrorRateQuery: profile.ErrorRateQuery, LatencyQuery: profile.LatencyQuery, QueueQuery: profile.QueueQuery,
			ReplicaQuery: profile.ReplicaQuery, LogsQuery: profile.LogsQuery, DeployLogsQuery: profile.DeployLogsQuery,
			Lookback: cfg.GrafanaQueryLookback,
		}, observability.NewQueryRenderer())
		evidence, err := collector.Collect(r.Context(), domain.Incident{ID: "grafana-preview", ServiceName: "queue-worker", Environment: "staging", Severity: "critical", Title: "Grafana onboarding preview"})
		if err != nil {
			writeAPIError(w, http.StatusBadGateway, "query_preview_failed", "Grafana queries could not be normalized into evidence. Check query templates and server logs.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "evidence": evidence})
	})
}

func registerPlaygroundAPI(mux *http.ServeMux, cfg config.Config) {
	mux.HandleFunc("/api/v1/playground", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		if strings.TrimSpace(cfg.WorkloadControlBaseURL) == "" {
			writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "modes": []string{"healthy", "stall", "fail"}})
			return
		}
		state, err := requestWorkloadControl(r.Context(), cfg, http.MethodGet, "/state", nil)
		if err != nil {
			writeAPIError(w, http.StatusBadGateway, "playground_disconnected", "The workload supervisor is not reachable.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "modes": []string{"healthy", "stall", "fail"}, "state": state})
	})

	mux.HandleFunc("/api/v1/playground/faults", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		if strings.TrimSpace(cfg.WorkloadControlBaseURL) == "" {
			writeAPIError(w, http.StatusServiceUnavailable, "playground_unavailable", "The workload supervisor is not configured.")
			return
		}
		var payload struct {
			Mode string `json:"mode"`
		}
		if err := decodeBoundedJSON(w, r, &payload); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		payload.Mode = strings.ToLower(strings.TrimSpace(payload.Mode))
		if payload.Mode != "healthy" && payload.Mode != "stall" && payload.Mode != "fail" {
			writeAPIError(w, http.StatusBadRequest, "invalid_fault", "Fault mode must be healthy, stall, or fail.")
			return
		}
		result, err := requestWorkloadControl(r.Context(), cfg, http.MethodPost, "/faults", payload)
		if err != nil {
			writeAPIError(w, http.StatusBadGateway, "fault_injection_failed", "The workload supervisor rejected the fault request.")
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
}

func effectiveReasoningProfile(ctx context.Context, cfg config.Config, settings storage.SettingsStore) config.ReasoningConnectionProfile {
	provider, baseURL, _, model := cfg.EffectiveLLM()
	reference := "LLM_API_KEY"
	profile := config.ReasoningConnectionProfile{Provider: provider, BaseURL: baseURL, Model: model, CredentialRef: reference, JSONMode: cfg.LLMJSONMode}
	if settings != nil {
		if bundle, ok := effectiveReasoningBundle(ctx, settings); ok {
			return bundle.Profile
		}
		if raw, err := settings.GetSetting(ctx, config.ReasoningConnectionSettingKey); err == nil {
			if stored, decodeErr := config.DecodeReasoningConnectionProfile(raw); decodeErr == nil {
				profile = stored
			}
		}
	}
	return profile
}

func effectiveReasoningBundle(ctx context.Context, settings storage.SettingsStore) (config.ReasoningConnectionBundle, bool) {
	if settings == nil {
		return config.ReasoningConnectionBundle{}, false
	}
	raw, err := settings.GetSetting(ctx, config.ReasoningConnectionBundleSettingKey)
	if err != nil {
		return config.ReasoningConnectionBundle{}, false
	}
	bundle, err := config.DecodeReasoningConnectionBundle(raw)
	return bundle, err == nil
}

func effectiveGrafanaProfile(ctx context.Context, cfg config.Config, settings storage.SettingsStore) config.GrafanaConnectionProfile {
	profile := config.GrafanaConnectionProfile{
		BaseURL: cfg.GrafanaBaseURL, CredentialRef: "GRAFANA_API_TOKEN", MetricsSourceUID: cfg.GrafanaMetricsSourceUID,
		LogsSourceUID: cfg.GrafanaLogsSourceUID, ErrorRateQuery: cfg.GrafanaErrorRateQuery, LatencyQuery: cfg.GrafanaLatencyQuery,
		QueueQuery: cfg.GrafanaQueueQuery, ReplicaQuery: cfg.GrafanaReplicaQuery, LogsQuery: cfg.GrafanaLogsQuery, DeployLogsQuery: cfg.GrafanaDeployLogsQuery,
	}
	if settings != nil {
		if raw, err := settings.GetSetting(ctx, config.GrafanaConnectionSettingKey); err == nil {
			if stored, decodeErr := config.DecodeGrafanaConnectionProfile(raw); decodeErr == nil {
				profile = stored
			}
		}
	}
	return profile
}

func reasoningProviderConfig(cfg config.Config, profile config.ReasoningConnectionProfile, apiKey string) ai.ProviderConfig {
	return ai.ProviderConfig{
		Name: profile.Provider, BaseURL: profile.BaseURL, APIKey: apiKey, Model: profile.Model,
		JSONMode: profile.JSONMode, Timeout: cfg.LLMTimeout,
		AllowHTTP: !cfg.InternalMode(), AllowHosts: cfg.LLMAllowHosts, RequireAllowlist: cfg.InternalMode(),
	}
}

func reasoningProfileResponse(profile config.ReasoningConnectionProfile, runtime *RuntimeControls, persisted bool) map[string]any {
	_, available := config.ResolveCredential(profile.CredentialRef)
	source := "environment"
	if persisted {
		available = runtime != nil && runtime.Reasoning != nil && runtime.Reasoning.Configured()
		source = "encrypted_store"
	} else if runtime != nil && runtime.Reasoning != nil && runtime.Reasoning.Configured() {
		available = true
		source = "runtime_memory"
	}
	if !available {
		source = "missing"
	}
	return map[string]any{"provider": profile.Provider, "base_url": profile.BaseURL, "model": profile.Model, "credential_ref": profile.CredentialRef, "credential_available": available, "credential_source": source, "json_mode": profile.JSONMode}
}

func grafanaProfileResponse(profile config.GrafanaConnectionProfile) map[string]any {
	_, available := config.ResolveCredential(profile.CredentialRef)
	body, _ := json.Marshal(profile)
	var payload map[string]any
	_ = json.Unmarshal(body, &payload)
	payload["credential_available"] = available
	return payload
}

func decodeBoundedJSON(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid JSON request")
	}
	return nil
}

func requestWorkloadControl(ctx context.Context, cfg config.Config, method string, path string, payload any) (map[string]any, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(cfg.WorkloadControlBaseURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+cfg.WorkloadControlToken)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, errors.New("workload control request failed")
	}
	var result map[string]any
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}
