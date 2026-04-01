package demo

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

type Config struct {
	Address string
}

type Mode string

const (
	ModeHealthy            Mode = "healthy"
	ModeErrorRateSpike     Mode = "error_rate_spike"
	ModeWorkerStall        Mode = "worker_stall"
	ModeTimeoutAfterDeploy Mode = "timeout_after_deploy"
)

type State struct {
	mu             sync.RWMutex
	Mode           Mode
	ErrorRate      float64
	LatencyMs      int
	QueueBacklog   int
	ReplicaCount   int
	ConsumerPaused bool
	WorkerHealthy  bool
	LastDeploy     string
	LastUpdatedUTC time.Time
}

type Snapshot struct {
	Mode           Mode      `json:"mode"`
	ErrorRate      float64   `json:"error_rate"`
	LatencyMs      int       `json:"latency_ms"`
	QueueBacklog   int       `json:"queue_backlog"`
	ReplicaCount   int       `json:"replica_count"`
	ConsumerPaused bool      `json:"consumer_paused"`
	WorkerHealthy  bool      `json:"worker_healthy"`
	LastDeploy     string    `json:"last_deploy"`
	LastUpdatedUTC time.Time `json:"last_updated_utc"`
}

func NewServer(cfg Config, logger *slog.Logger) *http.Server {
	state := &State{
		Mode:           ModeHealthy,
		ErrorRate:      0.01,
		LatencyMs:      120,
		QueueBacklog:   0,
		ReplicaCount:   2,
		ConsumerPaused: false,
		WorkerHealthy:  true,
		LastDeploy:     "v1.0.0",
		LastUpdatedUTC: time.Now().UTC(),
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		snapshot := state.snapshot()
		statusCode := http.StatusOK
		if snapshot.Mode != ModeHealthy {
			statusCode = http.StatusServiceUnavailable
		}

		writeJSON(w, statusCode, map[string]any{
			"status":          mapHealthStatus(snapshot),
			"mode":            snapshot.Mode,
			"error_rate":      snapshot.ErrorRate,
			"latency_ms":      snapshot.LatencyMs,
			"queue_backlog":   snapshot.QueueBacklog,
			"replica_count":   snapshot.ReplicaCount,
			"consumer_paused": snapshot.ConsumerPaused,
			"worker_healthy":  snapshot.WorkerHealthy,
			"last_deploy":     snapshot.LastDeploy,
		})
	})

	mux.HandleFunc("/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, state.snapshot())
	})

	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		snapshot := state.snapshot()

		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintf(w, "triovexa_demo_error_rate %.2f\n", snapshot.ErrorRate)
		_, _ = fmt.Fprintf(w, "triovexa_demo_latency_ms %d\n", snapshot.LatencyMs)
		_, _ = fmt.Fprintf(w, "triovexa_demo_queue_backlog %d\n", snapshot.QueueBacklog)
		_, _ = fmt.Fprintf(w, "triovexa_demo_replica_count %d\n", snapshot.ReplicaCount)
		_, _ = fmt.Fprintf(w, "triovexa_demo_consumer_paused %d\n", boolToFloat(snapshot.ConsumerPaused))
		_, _ = fmt.Fprintf(w, "triovexa_demo_worker_healthy %d\n", boolToFloat(snapshot.WorkerHealthy))
		_, _ = fmt.Fprintf(w, "triovexa_demo_mode{mode=%q} 1\n", snapshot.Mode)
	})

	mux.HandleFunc("/simulate/error-rate-spike", func(w http.ResponseWriter, r *http.Request) {
		state.apply(Snapshot{
			Mode:           ModeErrorRateSpike,
			ErrorRate:      0.38,
			LatencyMs:      850,
			QueueBacklog:   12,
			ReplicaCount:   2,
			ConsumerPaused: false,
			WorkerHealthy:  true,
			LastDeploy:     "v1.0.1",
			LastUpdatedUTC: time.Now().UTC(),
		})
		logger.Warn("demo incident triggered", slog.String("mode", string(ModeErrorRateSpike)))
		writeJSON(w, http.StatusAccepted, state.snapshot())
	})

	mux.HandleFunc("/simulate/worker-stall", func(w http.ResponseWriter, r *http.Request) {
		state.apply(Snapshot{
			Mode:           ModeWorkerStall,
			ErrorRate:      0.12,
			LatencyMs:      430,
			QueueBacklog:   128,
			ReplicaCount:   2,
			ConsumerPaused: false,
			WorkerHealthy:  false,
			LastDeploy:     "v1.0.1",
			LastUpdatedUTC: time.Now().UTC(),
		})
		logger.Warn("demo incident triggered", slog.String("mode", string(ModeWorkerStall)))
		writeJSON(w, http.StatusAccepted, state.snapshot())
	})

	mux.HandleFunc("/simulate/timeout-after-deploy", func(w http.ResponseWriter, r *http.Request) {
		state.apply(Snapshot{
			Mode:           ModeTimeoutAfterDeploy,
			ErrorRate:      0.27,
			LatencyMs:      1250,
			QueueBacklog:   46,
			ReplicaCount:   2,
			ConsumerPaused: false,
			WorkerHealthy:  true,
			LastDeploy:     "v1.1.0",
			LastUpdatedUTC: time.Now().UTC(),
		})
		logger.Warn("demo incident triggered", slog.String("mode", string(ModeTimeoutAfterDeploy)))
		writeJSON(w, http.StatusAccepted, state.snapshot())
	})

	mux.HandleFunc("/simulate/reset", func(w http.ResponseWriter, r *http.Request) {
		state.apply(Snapshot{
			Mode:           ModeHealthy,
			ErrorRate:      0.01,
			LatencyMs:      120,
			QueueBacklog:   0,
			ReplicaCount:   state.snapshot().ReplicaCount,
			ConsumerPaused: false,
			WorkerHealthy:  true,
			LastDeploy:     state.snapshot().LastDeploy,
			LastUpdatedUTC: time.Now().UTC(),
		})
		logger.Info("demo incident state reset")
		writeJSON(w, http.StatusAccepted, state.snapshot())
	})

	mux.HandleFunc("/actions/restart-worker", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var payload struct {
			WorkerID string `json:"worker_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload.WorkerID == "" {
			payload.WorkerID = "worker-primary"
		}

		state.apply(Snapshot{
			Mode:           ModeHealthy,
			ErrorRate:      0.04,
			LatencyMs:      190,
			QueueBacklog:   24,
			ReplicaCount:   state.snapshot().ReplicaCount,
			ConsumerPaused: false,
			WorkerHealthy:  true,
			LastDeploy:     state.snapshot().LastDeploy,
			LastUpdatedUTC: time.Now().UTC(),
		})
		logger.Info("demo action executed", slog.String("action", "restart_demo_worker"), slog.String("worker_id", payload.WorkerID))
		writeJSON(w, http.StatusOK, map[string]any{
			"action":    "restart_demo_worker",
			"worker_id": payload.WorkerID,
			"applied":   true,
			"snapshot":  state.snapshot(),
		})
	})

	mux.HandleFunc("/actions/retry-job", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var payload struct {
			JobID string `json:"job_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload.JobID == "" {
			payload.JobID = "backlog-drain-batch"
		}

		state.apply(Snapshot{
			Mode:           ModeHealthy,
			ErrorRate:      0.03,
			LatencyMs:      170,
			QueueBacklog:   8,
			ReplicaCount:   state.snapshot().ReplicaCount,
			ConsumerPaused: false,
			WorkerHealthy:  true,
			LastDeploy:     state.snapshot().LastDeploy,
			LastUpdatedUTC: time.Now().UTC(),
		})
		logger.Info("demo action executed", slog.String("action", "retry_demo_background_job"), slog.String("job_id", payload.JobID))
		writeJSON(w, http.StatusOK, map[string]any{
			"action":   "retry_demo_background_job",
			"job_id":   payload.JobID,
			"applied":  true,
			"snapshot": state.snapshot(),
		})
	})

	mux.HandleFunc("/actions/refresh-cache", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var payload struct {
			CacheKey string `json:"cache_key"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload.CacheKey == "" {
			payload.CacheKey = "all"
		}

		state.apply(Snapshot{
			Mode:           ModeHealthy,
			ErrorRate:      0.02,
			LatencyMs:      150,
			QueueBacklog:   4,
			ReplicaCount:   state.snapshot().ReplicaCount,
			ConsumerPaused: false,
			WorkerHealthy:  true,
			LastDeploy:     state.snapshot().LastDeploy,
			LastUpdatedUTC: time.Now().UTC(),
		})
		logger.Info("demo action executed", slog.String("action", "refresh_demo_cache"), slog.String("cache_key", payload.CacheKey))
		writeJSON(w, http.StatusOK, map[string]any{
			"action":    "refresh_demo_cache",
			"cache_key": payload.CacheKey,
			"applied":   true,
			"snapshot":  state.snapshot(),
		})
	})

	mux.HandleFunc("/actions/pause-queue-consumer", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		state.apply(Snapshot{
			Mode:           ModeWorkerStall,
			ErrorRate:      0.18,
			LatencyMs:      690,
			QueueBacklog:   160,
			ReplicaCount:   state.snapshot().ReplicaCount,
			ConsumerPaused: true,
			WorkerHealthy:  false,
			LastDeploy:     state.snapshot().LastDeploy,
			LastUpdatedUTC: time.Now().UTC(),
		})
		logger.Warn("demo action executed", slog.String("action", "pause_demo_queue_consumer"))
		writeJSON(w, http.StatusOK, map[string]any{
			"action":   "pause_demo_queue_consumer",
			"applied":  true,
			"snapshot": state.snapshot(),
		})
	})

	mux.HandleFunc("/actions/resume-queue-consumer", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		state.apply(Snapshot{
			Mode:           ModeHealthy,
			ErrorRate:      0.02,
			LatencyMs:      180,
			QueueBacklog:   10,
			ReplicaCount:   state.snapshot().ReplicaCount,
			ConsumerPaused: false,
			WorkerHealthy:  true,
			LastDeploy:     state.snapshot().LastDeploy,
			LastUpdatedUTC: time.Now().UTC(),
		})
		logger.Info("demo action executed", slog.String("action", "resume_demo_queue_consumer"))
		writeJSON(w, http.StatusOK, map[string]any{
			"action":   "resume_demo_queue_consumer",
			"applied":  true,
			"snapshot": state.snapshot(),
		})
	})

	return &http.Server{
		Addr:              cfg.Address,
		Handler:           withLogging(logger, mux),
		ReadHeaderTimeout: 3 * time.Second,
	}
}

func (s *State) apply(next Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Mode = next.Mode
	s.ErrorRate = next.ErrorRate
	s.LatencyMs = next.LatencyMs
	s.QueueBacklog = next.QueueBacklog
	s.ReplicaCount = next.ReplicaCount
	s.ConsumerPaused = next.ConsumerPaused
	s.WorkerHealthy = next.WorkerHealthy
	s.LastDeploy = next.LastDeploy
	s.LastUpdatedUTC = next.LastUpdatedUTC
}

func (s *State) snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return Snapshot{
		Mode:           s.Mode,
		ErrorRate:      s.ErrorRate,
		LatencyMs:      s.LatencyMs,
		QueueBacklog:   s.QueueBacklog,
		ReplicaCount:   s.ReplicaCount,
		ConsumerPaused: s.ConsumerPaused,
		WorkerHealthy:  s.WorkerHealthy,
		LastDeploy:     s.LastDeploy,
		LastUpdatedUTC: s.LastUpdatedUTC,
	}
}

func mapHealthStatus(snapshot Snapshot) string {
	if snapshot.Mode == ModeHealthy {
		return "ok"
	}

	return "degraded"
}

func boolToFloat(value bool) int {
	if value {
		return 1
	}

	return 0
}

func withLogging(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		next.ServeHTTP(w, r)
		logger.Info("demo http request completed",
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
