package config

import (
	"log/slog"
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
	ServiceName       string
	Environment       string
	HTTPPort          string
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	KillSwitchEnabled bool
	LogLevel          slog.Level
}

func Load() Config {
	return Config{
		ServiceName:       getEnv("APP_NAME", "triovexa"),
		Environment:       getEnv("APP_ENV", defaultEnvironment),
		HTTPPort:          getEnv("HTTP_PORT", defaultHTTPPort),
		ReadTimeout:       getDurationEnv("HTTP_READ_TIMEOUT", defaultReadTimeout),
		WriteTimeout:      getDurationEnv("HTTP_WRITE_TIMEOUT", defaultWriteTimeout),
		IdleTimeout:       getDurationEnv("HTTP_IDLE_TIMEOUT", defaultIdleTimeout),
		ShutdownTimeout:   getDurationEnv("HTTP_SHUTDOWN_TIMEOUT", defaultShutdownTimeout),
		KillSwitchEnabled: getBoolEnv("KILL_SWITCH_ENABLED", false),
		LogLevel:          getLogLevelEnv("LOG_LEVEL", slog.LevelInfo),
	}
}

func (c Config) HTTPAddress() string {
	return ":" + c.HTTPPort
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
