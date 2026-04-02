package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ObservabilityProfile struct {
	BaseURL          string `json:"grafana_base_url"`
	APIToken         string `json:"grafana_api_token"`
	MetricsSourceUID string `json:"grafana_metrics_datasource_uid"`
	LogsSourceUID    string `json:"grafana_logs_datasource_uid"`
	ErrorRateQuery   string `json:"grafana_error_rate_query"`
	LatencyQuery     string `json:"grafana_latency_query"`
	QueueQuery       string `json:"grafana_queue_query"`
	ReplicaQuery     string `json:"grafana_replica_query"`
	LogsQuery        string `json:"grafana_logs_query"`
	DeployLogsQuery  string `json:"grafana_deploy_logs_query"`
}

func DefaultObservabilityProfilePath() string {
	configDir, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(configDir) == "" {
		return filepath.Join(".triovexa", "observability-profile.json")
	}

	return filepath.Join(configDir, "Triovexa", "observability-profile.json")
}

func LoadObservabilityProfile(path string) (ObservabilityProfile, bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return ObservabilityProfile{}, false, nil
	}

	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ObservabilityProfile{}, false, nil
		}
		return ObservabilityProfile{}, false, fmt.Errorf("read observability profile: %w", err)
	}

	var profile ObservabilityProfile
	if err := json.Unmarshal(content, &profile); err != nil {
		return ObservabilityProfile{}, false, fmt.Errorf("decode observability profile: %w", err)
	}

	profile = normalizeObservabilityProfile(profile)
	return profile, true, nil
}

func SaveObservabilityProfile(path string, profile ObservabilityProfile) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("observability profile path is empty")
	}

	profile = normalizeObservabilityProfile(profile)
	content, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return fmt.Errorf("encode observability profile: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create observability profile directory: %w", err)
	}
	if err := os.WriteFile(path, append(content, '\n'), 0o600); err != nil {
		return fmt.Errorf("write observability profile: %w", err)
	}

	return nil
}

func ClearObservabilityProfile(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove observability profile: %w", err)
	}
	return nil
}

func normalizeObservabilityProfile(profile ObservabilityProfile) ObservabilityProfile {
	profile.BaseURL = strings.TrimSpace(profile.BaseURL)
	profile.APIToken = strings.TrimSpace(profile.APIToken)
	profile.MetricsSourceUID = strings.TrimSpace(profile.MetricsSourceUID)
	profile.LogsSourceUID = strings.TrimSpace(profile.LogsSourceUID)
	profile.ErrorRateQuery = strings.TrimSpace(profile.ErrorRateQuery)
	profile.LatencyQuery = strings.TrimSpace(profile.LatencyQuery)
	profile.QueueQuery = strings.TrimSpace(profile.QueueQuery)
	profile.ReplicaQuery = strings.TrimSpace(profile.ReplicaQuery)
	profile.LogsQuery = strings.TrimSpace(profile.LogsQuery)
	profile.DeployLogsQuery = strings.TrimSpace(profile.DeployLogsQuery)
	return profile
}
