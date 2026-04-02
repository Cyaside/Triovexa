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
	defaultEnvironment     = "local"
	defaultReadTimeout     = 5 * time.Second
	defaultWriteTimeout    = 10 * time.Second
	defaultIdleTimeout     = 30 * time.Second
	defaultShutdownTimeout = 10 * time.Second
)

type Config struct {
	ServiceName             string
	Environment             string
	HTTPPort                string
	DatabaseURL             string
	DocsRoot                string
	DemoServiceBaseURL      string
	ActionExecutionTimeout  time.Duration
	ActionExecutionCooldown time.Duration
	ActionExecutionRetries  int
	ReadTimeout             time.Duration
	WriteTimeout            time.Duration
	IdleTimeout             time.Duration
	ShutdownTimeout         time.Duration
	KillSwitchEnabled       bool
	LogLevel                slog.Level
}

func Load() Config {
	return Config{
		ServiceName:             getEnv("APP_NAME", "triovexa"),
		Environment:             getEnv("APP_ENV", defaultEnvironment),
		HTTPPort:                getEnv("HTTP_PORT", defaultHTTPPort),
		DatabaseURL:             getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/triovexa?sslmode=disable"),
		DocsRoot:                getEnv("DOCS_ROOT", "docs"),
		DemoServiceBaseURL:      getEnv("DEMO_SERVICE_BASE_URL", "http://localhost:8090"),
		ActionExecutionTimeout:  getDurationEnv("ACTION_EXECUTION_TIMEOUT", 5*time.Second),
		ActionExecutionCooldown: getDurationEnv("ACTION_EXECUTION_COOLDOWN", time.Minute),
		ActionExecutionRetries:  getIntEnv("ACTION_EXECUTION_RETRIES", 1),
		ReadTimeout:             getDurationEnv("HTTP_READ_TIMEOUT", defaultReadTimeout),
		WriteTimeout:            getDurationEnv("HTTP_WRITE_TIMEOUT", defaultWriteTimeout),
		IdleTimeout:             getDurationEnv("HTTP_IDLE_TIMEOUT", defaultIdleTimeout),
		ShutdownTimeout:         getDurationEnv("HTTP_SHUTDOWN_TIMEOUT", defaultShutdownTimeout),
		KillSwitchEnabled:       getBoolEnv("KILL_SWITCH_ENABLED", false),
		LogLevel:                getLogLevelEnv("LOG_LEVEL", slog.LevelInfo),
	}
}

func (c Config) HTTPAddress() string {
	return ":" + c.HTTPPort
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
