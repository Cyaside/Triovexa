package http

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/Cyaside/Triovexa/internal/config"
)

type ServerInfo struct {
	Name               string   `json:"name"`
	Environment        string   `json:"environment"`
	KillSwitchEnabled  bool     `json:"kill_switch_enabled"`
	Phase              string   `json:"phase"`
	AvailableEndpoints []string `json:"available_endpoints"`
	Timestamp          string   `json:"timestamp"`
}

func NewServer(cfg config.Config, logger *slog.Logger) *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":      "ok",
			"service":     cfg.ServiceName,
			"environment": cfg.Environment,
		})
	})

	mux.HandleFunc("/debug/tools", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, ServerInfo{
			Name:              cfg.ServiceName,
			Environment:       cfg.Environment,
			KillSwitchEnabled: cfg.KillSwitchEnabled,
			Phase:             "phase-00-foundation",
			AvailableEndpoints: []string{
				"GET /health",
				"GET /debug/tools",
			},
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
	})

	return &http.Server{
		Addr:         cfg.HTTPAddress(),
		Handler:      withLogging(logger, mux),
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}
}

func withLogging(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		next.ServeHTTP(w, r)
		logger.Info("http request completed",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Duration("duration", time.Since(startedAt)),
		)
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
