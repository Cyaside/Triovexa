package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDatabaseTarget(t *testing.T) {
	t.Run("returns memory label for in-memory mode", func(t *testing.T) {
		cfg := Config{DatabaseURL: "memory"}

		if got := cfg.DatabaseTarget(); got != "memory" {
			t.Fatalf("DatabaseTarget() = %q, want %q", got, "memory")
		}
	})

	t.Run("returns host and database for postgres url", func(t *testing.T) {
		cfg := Config{DatabaseURL: "postgres://postgres:postgres@localhost:5432/triovexa?sslmode=disable"}

		if got := cfg.DatabaseTarget(); got != "localhost:5432/triovexa" {
			t.Fatalf("DatabaseTarget() = %q, want %q", got, "localhost:5432/triovexa")
		}
	})
}

func TestLoadConfigIncludesProviderDefaults(t *testing.T) {
	t.Setenv("REASONING_MODE", "")
	t.Setenv("OBSERVABILITY_MODE", "")
	t.Setenv("GRAFANA_METRICS_DATASOURCE_UID", "")
	t.Setenv("GRAFANA_LOGS_DATASOURCE_UID", "")

	cfg := Load()

	if cfg.ReasoningMode != "llm" {
		t.Fatalf("ReasoningMode = %q, want %q", cfg.ReasoningMode, "llm")
	}
	if cfg.ObservabilityMode != "demo" {
		t.Fatalf("ObservabilityMode = %q, want %q", cfg.ObservabilityMode, "demo")
	}
	provider, baseURL, apiKey, model := cfg.EffectiveLLM()
	if provider != "openai-compatible" || baseURL != "" || apiKey != "" || model != "" {
		t.Fatalf("EffectiveLLM() = %q %q %q %q", provider, baseURL, apiKey, model)
	}
	if cfg.GrafanaMetricsSourceUID != "grafanacloud-prom" {
		t.Fatalf("GrafanaMetricsSourceUID = %q, want %q", cfg.GrafanaMetricsSourceUID, "grafanacloud-prom")
	}
	if cfg.GrafanaLogsSourceUID != "grafanacloud-logs" {
		t.Fatalf("GrafanaLogsSourceUID = %q, want %q", cfg.GrafanaLogsSourceUID, "grafanacloud-logs")
	}
	if cfg.WriteTimeout != 150*time.Second {
		t.Fatalf("WriteTimeout = %v, want %v", cfg.WriteTimeout, 150*time.Second)
	}
	if cfg.LLMTimeout != 60*time.Second {
		t.Fatalf("LLMTimeout = %v, want %v", cfg.LLMTimeout, 60*time.Second)
	}
}

func TestLoadUsesLocalObservabilityProfileWhenEnvUnset(t *testing.T) {
	profilePath := filepath.Join(t.TempDir(), "observability-profile.json")
	if err := SaveObservabilityProfile(profilePath, ObservabilityProfile{
		BaseURL:          "https://grafana.example.com",
		APIToken:         "local-token",
		MetricsSourceUID: "metrics-uid",
		LogsSourceUID:    "logs-uid",
		ErrorRateQuery:   "vector(0.12)",
	}); err != nil {
		t.Fatalf("SaveObservabilityProfile() error = %v", err)
	}

	t.Setenv("LOCAL_OBSERVABILITY_PROFILE_PATH", profilePath)
	t.Setenv("GRAFANA_BASE_URL", "")
	t.Setenv("GRAFANA_API_TOKEN", "")
	t.Setenv("GRAFANA_METRICS_DATASOURCE_UID", "")
	t.Setenv("GRAFANA_LOGS_DATASOURCE_UID", "")
	t.Setenv("GRAFANA_ERROR_RATE_QUERY", "")

	cfg := Load()

	if cfg.GrafanaBaseURL != "https://grafana.example.com" {
		t.Fatalf("GrafanaBaseURL = %q, want profile value", cfg.GrafanaBaseURL)
	}
	if cfg.GrafanaAPIToken != "local-token" {
		t.Fatalf("GrafanaAPIToken = %q, want profile value", cfg.GrafanaAPIToken)
	}
	if cfg.GrafanaMetricsSourceUID != "metrics-uid" {
		t.Fatalf("GrafanaMetricsSourceUID = %q, want profile value", cfg.GrafanaMetricsSourceUID)
	}
	if cfg.GrafanaLogsSourceUID != "logs-uid" {
		t.Fatalf("GrafanaLogsSourceUID = %q, want profile value", cfg.GrafanaLogsSourceUID)
	}
	if cfg.GrafanaErrorRateQuery != "vector(0.12)" {
		t.Fatalf("GrafanaErrorRateQuery = %q, want profile value", cfg.GrafanaErrorRateQuery)
	}
	if !cfg.LocalObservabilityProfileLoaded {
		t.Fatalf("LocalObservabilityProfileLoaded = false, want true")
	}
}

func TestLoadPrefersEnvOverLocalObservabilityProfile(t *testing.T) {
	profilePath := filepath.Join(t.TempDir(), "observability-profile.json")
	if err := SaveObservabilityProfile(profilePath, ObservabilityProfile{
		BaseURL:        "https://grafana.example.com",
		APIToken:       "local-token",
		ErrorRateQuery: "vector(0.12)",
	}); err != nil {
		t.Fatalf("SaveObservabilityProfile() error = %v", err)
	}

	t.Setenv("LOCAL_OBSERVABILITY_PROFILE_PATH", profilePath)
	t.Setenv("GRAFANA_BASE_URL", "https://override.example.com")
	t.Setenv("GRAFANA_API_TOKEN", "env-token")
	t.Setenv("GRAFANA_ERROR_RATE_QUERY", "vector(0.50)")

	cfg := Load()

	if cfg.GrafanaBaseURL != "https://override.example.com" {
		t.Fatalf("GrafanaBaseURL = %q, want env override", cfg.GrafanaBaseURL)
	}
	if cfg.GrafanaAPIToken != "env-token" {
		t.Fatalf("GrafanaAPIToken = %q, want env override", cfg.GrafanaAPIToken)
	}
	if cfg.GrafanaErrorRateQuery != "vector(0.50)" {
		t.Fatalf("GrafanaErrorRateQuery = %q, want env override", cfg.GrafanaErrorRateQuery)
	}
}

func TestObservabilityProfileRoundTrip(t *testing.T) {
	profilePath := filepath.Join(t.TempDir(), "observability-profile.json")
	input := ObservabilityProfile{
		BaseURL:          " https://grafana.example.com ",
		APIToken:         " secret-token ",
		MetricsSourceUID: " prom-uid ",
		LogsSourceUID:    " logs-uid ",
	}

	if err := SaveObservabilityProfile(profilePath, input); err != nil {
		t.Fatalf("SaveObservabilityProfile() error = %v", err)
	}

	info, err := os.Stat(profilePath)
	if err != nil {
		t.Fatalf("Stat(profilePath) error = %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("saved profile file should not be empty")
	}

	loaded, ok, err := LoadObservabilityProfile(profilePath)
	if err != nil {
		t.Fatalf("LoadObservabilityProfile() error = %v", err)
	}
	if !ok {
		t.Fatalf("LoadObservabilityProfile() loaded = false, want true")
	}
	if loaded.BaseURL != "https://grafana.example.com" {
		t.Fatalf("BaseURL = %q, want trimmed value", loaded.BaseURL)
	}
	if loaded.APIToken != "secret-token" {
		t.Fatalf("APIToken = %q, want trimmed value", loaded.APIToken)
	}

	if err := ClearObservabilityProfile(profilePath); err != nil {
		t.Fatalf("ClearObservabilityProfile() error = %v", err)
	}
	if _, ok, err := LoadObservabilityProfile(profilePath); err != nil || ok {
		t.Fatalf("profile should be cleared, got ok=%v err=%v", ok, err)
	}
}
