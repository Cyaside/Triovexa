package config

import (
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
	t.Setenv("MISTRAL_MODEL", "")
	t.Setenv("GRAFANA_METRICS_DATASOURCE_UID", "")
	t.Setenv("GRAFANA_LOGS_DATASOURCE_UID", "")

	cfg := Load()

	if cfg.ReasoningMode != "heuristic" {
		t.Fatalf("ReasoningMode = %q, want %q", cfg.ReasoningMode, "heuristic")
	}
	if cfg.ObservabilityMode != "demo" {
		t.Fatalf("ObservabilityMode = %q, want %q", cfg.ObservabilityMode, "demo")
	}
	if cfg.MistralModel != "mistral-small-latest" {
		t.Fatalf("MistralModel = %q, want %q", cfg.MistralModel, "mistral-small-latest")
	}
	if cfg.GrafanaMetricsSourceUID != "grafanacloud-prom" {
		t.Fatalf("GrafanaMetricsSourceUID = %q, want %q", cfg.GrafanaMetricsSourceUID, "grafanacloud-prom")
	}
	if cfg.GrafanaLogsSourceUID != "grafanacloud-logs" {
		t.Fatalf("GrafanaLogsSourceUID = %q, want %q", cfg.GrafanaLogsSourceUID, "grafanacloud-logs")
	}
	if cfg.WriteTimeout != 30*time.Second {
		t.Fatalf("WriteTimeout = %v, want %v", cfg.WriteTimeout, 30*time.Second)
	}
}
