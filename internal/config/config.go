package config

import (
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHTTPPort        = "8080"
	defaultHTTPHost        = "127.0.0.1"
	defaultEnvironment     = "local"
	defaultReadTimeout     = 5 * time.Second
	defaultWriteTimeout    = 30 * time.Second
	defaultIdleTimeout     = 30 * time.Second
	defaultShutdownTimeout = 10 * time.Second
)

type Config struct {
	ServiceName                     string
	Environment                     string
	HTTPPort                        string
	HTTPHost                        string
	DeploymentMode                  string
	SessionSecure                   bool
	SessionTTL                      time.Duration
	GrafanaWebhookSecret            string
	DatabaseURL                     string
	DocsRoot                        string
	DemoServiceBaseURL              string
	WorkloadControlBaseURL          string
	WorkloadControlToken            string
	ReasoningMode                   string
	ObservabilityMode               string
	MistralAPIKey                   string
	MistralModel                    string
	LLMProvider                     string
	LLMBaseURL                      string
	LLMAPIKey                       string
	LLMModel                        string
	LLMJSONMode                     bool
	LLMTimeout                      time.Duration
	LLMAllowHosts                   []string
	GrafanaBaseURL                  string
	GrafanaAPIToken                 string
	GrafanaMetricsSourceUID         string
	GrafanaLogsSourceUID            string
	GrafanaErrorRateQuery           string
	GrafanaLatencyQuery             string
	GrafanaQueueQuery               string
	GrafanaReplicaQuery             string
	GrafanaLogsQuery                string
	GrafanaDeployLogsQuery          string
	GrafanaQueryLookback            time.Duration
	PrometheusBaseURL               string
	AlertmanagerBaseURL             string
	LocalObservabilityProfilePath   string
	LocalObservabilityProfileLoaded bool
	LocalObservabilityProfileError  string
	ActionExecutionTimeout          time.Duration
	ActionExecutionCooldown         time.Duration
	ActionExecutionRetries          int
	ReadTimeout                     time.Duration
	WriteTimeout                    time.Duration
	IdleTimeout                     time.Duration
	ShutdownTimeout                 time.Duration
	KillSwitchEnabled               bool
	LogLevel                        slog.Level
}

func Load() Config {
	profilePath := getEnv("LOCAL_OBSERVABILITY_PROFILE_PATH", DefaultObservabilityProfilePath())
	profile, profileLoaded, profileErr := LoadObservabilityProfile(profilePath)
	profileError := ""
	if profileErr != nil {
		profileError = profileErr.Error()
	}

	return Config{
		ServiceName:                     getEnv("APP_NAME", "triovexa"),
		Environment:                     getEnv("APP_ENV", defaultEnvironment),
		HTTPPort:                        getEnv("HTTP_PORT", defaultHTTPPort),
		HTTPHost:                        getEnv("HTTP_HOST", defaultHTTPHost),
		DeploymentMode:                  getEnv("DEPLOYMENT_MODE", "local-demo"),
		SessionSecure:                   getBoolEnv("SESSION_SECURE", false),
		SessionTTL:                      getDurationEnv("SESSION_TTL", 12*time.Hour),
		GrafanaWebhookSecret:            getEnv("GRAFANA_WEBHOOK_SECRET", ""),
		DatabaseURL:                     getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/triovexa?sslmode=disable"),
		DocsRoot:                        getEnv("DOCS_ROOT", "docs"),
		DemoServiceBaseURL:              getEnv("DEMO_SERVICE_BASE_URL", "http://localhost:8090"),
		WorkloadControlBaseURL:          getEnv("WORKLOAD_CONTROL_BASE_URL", ""),
		WorkloadControlToken:            getEnv("WORKLOAD_CONTROL_TOKEN", ""),
		ReasoningMode:                   getEnv("REASONING_MODE", "heuristic"),
		ObservabilityMode:               getEnv("OBSERVABILITY_MODE", "demo"),
		MistralAPIKey:                   getEnv("MISTRAL_API_KEY", ""),
		MistralModel:                    getEnv("MISTRAL_MODEL", "mistral-small-latest"),
		LLMProvider:                     getEnv("LLM_PROVIDER", ""),
		LLMBaseURL:                      getEnv("LLM_BASE_URL", ""),
		LLMAPIKey:                       getEnv("LLM_API_KEY", ""),
		LLMModel:                        getEnv("LLM_MODEL", ""),
		LLMJSONMode:                     getBoolEnv("LLM_JSON_MODE", true),
		LLMTimeout:                      getDurationEnv("LLM_TIMEOUT", 20*time.Second),
		LLMAllowHosts:                   splitCSVEnv("LLM_ALLOW_HOSTS"),
		GrafanaBaseURL:                  getEnvOrValue("GRAFANA_BASE_URL", profile.BaseURL, ""),
		GrafanaAPIToken:                 getEnvOrValue("GRAFANA_API_TOKEN", profile.APIToken, ""),
		GrafanaMetricsSourceUID:         getEnvOrValue("GRAFANA_METRICS_DATASOURCE_UID", profile.MetricsSourceUID, "grafanacloud-prom"),
		GrafanaLogsSourceUID:            getEnvOrValue("GRAFANA_LOGS_DATASOURCE_UID", profile.LogsSourceUID, "grafanacloud-logs"),
		GrafanaErrorRateQuery:           getEnvOrValue("GRAFANA_ERROR_RATE_QUERY", profile.ErrorRateQuery, ""),
		GrafanaLatencyQuery:             getEnvOrValue("GRAFANA_LATENCY_QUERY", profile.LatencyQuery, ""),
		GrafanaQueueQuery:               getEnvOrValue("GRAFANA_QUEUE_QUERY", profile.QueueQuery, ""),
		GrafanaReplicaQuery:             getEnvOrValue("GRAFANA_REPLICA_QUERY", profile.ReplicaQuery, ""),
		GrafanaLogsQuery:                getEnvOrValue("GRAFANA_LOGS_QUERY", profile.LogsQuery, ""),
		GrafanaDeployLogsQuery:          getEnvOrValue("GRAFANA_DEPLOY_LOGS_QUERY", profile.DeployLogsQuery, ""),
		GrafanaQueryLookback:            getDurationEnv("GRAFANA_QUERY_LOOKBACK", 15*time.Minute),
		PrometheusBaseURL:               getEnv("PROMETHEUS_BASE_URL", ""),
		AlertmanagerBaseURL:             getEnv("ALERTMANAGER_BASE_URL", ""),
		LocalObservabilityProfilePath:   profilePath,
		LocalObservabilityProfileLoaded: profileLoaded,
		LocalObservabilityProfileError:  profileError,
		ActionExecutionTimeout:          getDurationEnv("ACTION_EXECUTION_TIMEOUT", 5*time.Second),
		ActionExecutionCooldown:         getDurationEnv("ACTION_EXECUTION_COOLDOWN", time.Minute),
		ActionExecutionRetries:          getIntEnv("ACTION_EXECUTION_RETRIES", 1),
		ReadTimeout:                     getDurationEnv("HTTP_READ_TIMEOUT", defaultReadTimeout),
		WriteTimeout:                    getDurationEnv("HTTP_WRITE_TIMEOUT", defaultWriteTimeout),
		IdleTimeout:                     getDurationEnv("HTTP_IDLE_TIMEOUT", defaultIdleTimeout),
		ShutdownTimeout:                 getDurationEnv("HTTP_SHUTDOWN_TIMEOUT", defaultShutdownTimeout),
		KillSwitchEnabled:               getBoolEnv("KILL_SWITCH_ENABLED", false),
		LogLevel:                        getLogLevelEnv("LOG_LEVEL", slog.LevelInfo),
	}
}

func (c Config) EffectiveLLM() (provider string, baseURL string, apiKey string, model string) {
	provider = strings.ToLower(strings.TrimSpace(c.LLMProvider))
	baseURL = strings.TrimSpace(c.LLMBaseURL)
	apiKey = strings.TrimSpace(c.LLMAPIKey)
	model = strings.TrimSpace(c.LLMModel)
	if provider != "" || baseURL != "" || apiKey != "" || model != "" {
		if provider == "" {
			provider = "openai-compatible"
		}
		return provider, baseURL, apiKey, model
	}
	return "mistral", "https://api.mistral.ai", strings.TrimSpace(c.MistralAPIKey), strings.TrimSpace(c.MistralModel)
}

func (c Config) HTTPAddress() string {
	return strings.TrimSpace(c.HTTPHost) + ":" + c.HTTPPort
}

func (c Config) InternalMode() bool {
	return strings.EqualFold(strings.TrimSpace(c.DeploymentMode), "internal")
}

func (c Config) DatabaseTarget() string {
	target := strings.TrimSpace(c.DatabaseURL)
	if target == "" {
		return "unset"
	}
	if strings.EqualFold(target, "memory") {
		return "memory"
	}

	parsed, err := url.Parse(target)
	if err != nil {
		return "configured"
	}

	host := parsed.Host
	database := strings.TrimPrefix(parsed.Path, "/")
	if host == "" && database == "" {
		return "configured"
	}
	if database == "" {
		return host
	}
	if host == "" {
		return database
	}
	return host + "/" + database
}

func getEnv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}

	return fallback
}

func splitCSVEnv(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func getEnvOrValue(key string, preferred string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	if value := strings.TrimSpace(preferred); value != "" {
		return value
	}
	return fallback
}

func getBoolEnv(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}

	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}

	return parsed
}

func getDurationEnv(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}

	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}

	return parsed
}

func getLogLevelEnv(key string, fallback slog.Level) slog.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	case "info":
		return slog.LevelInfo
	default:
		return fallback
	}
}

func getIntEnv(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}

	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}

	return parsed
}
