package workload

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const stream = "triovexa:jobs"

func RedisClient(address string) *redis.Client { return redis.NewClient(&redis.Options{Addr: address}) }

func RunProducer(ctx context.Context, client *redis.Client, interval time.Duration, logger *slog.Logger) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C:
			id := uuid.NewString()
			if err := client.XAdd(ctx, &redis.XAddArgs{Stream: stream, Values: map[string]any{"job_id": id, "created_at": now.UTC().Format(time.RFC3339Nano)}}).Err(); err != nil {
				logger.Error("enqueue workload job", "error", err)
				continue
			}
			_ = client.Incr(ctx, "triovexa:stats:produced").Err()
		}
	}
}

func RunWorker(ctx context.Context, client *redis.Client, logger *slog.Logger) error {
	_ = client.XGroupCreateMkStream(ctx, stream, "workers", "0").Err()
	consumer := "worker-" + uuid.NewString()[:8]
	for {
		if ctx.Err() != nil {
			return nil
		}
		_ = client.Set(ctx, "triovexa:worker:heartbeat", time.Now().UTC().Format(time.RFC3339Nano), 15*time.Second).Err()
		paused, _ := client.Get(ctx, "triovexa:consumer:paused").Bool()
		fault, _ := client.Get(ctx, "triovexa:fault").Result()
		if paused || fault == "stall" {
			time.Sleep(time.Second)
			continue
		}
		messages, err := client.XReadGroup(ctx, &redis.XReadGroupArgs{Group: "workers", Consumer: consumer, Streams: []string{stream, ">"}, Count: 1, Block: 2 * time.Second}).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				continue
			}
			logger.Error("read job", "error", err)
			time.Sleep(time.Second)
			continue
		}
		for _, batch := range messages {
			for _, message := range batch.Messages {
				jobID := fmt.Sprint(message.Values["job_id"])
				if fault == "fail" {
					_ = client.Incr(ctx, "triovexa:stats:errors").Err()
					continue
				}
				first, _ := client.SetNX(ctx, "triovexa:processed:"+jobID, "1", 24*time.Hour).Result()
				if first {
					time.Sleep(150 * time.Millisecond)
					_ = client.Incr(ctx, "triovexa:stats:processed").Err()
				}
				_ = client.XAck(ctx, stream, "workers", message.ID).Err()
				_ = client.XDel(ctx, stream, message.ID).Err()
			}
		}
	}
}

type Supervisor struct {
	client     *redis.Client
	token      string
	logger     *slog.Logger
	mu         sync.Mutex
	child      *exec.Cmd
	generation atomic.Int64
}

func NewSupervisor(client *redis.Client, token string, logger *slog.Logger) *Supervisor {
	return &Supervisor{client: client, token: token, logger: logger}
}

func (s *Supervisor) StartWorker(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.child != nil && s.child.Process != nil {
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, executable, "worker")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return err
	}
	s.child = cmd
	s.generation.Add(1)
	go func(current *exec.Cmd) {
		_ = current.Wait()
		s.mu.Lock()
		if s.child == current {
			s.child = nil
		}
		s.mu.Unlock()
	}(cmd)
	return nil
}

func (s *Supervisor) restart(ctx context.Context) error {
	s.mu.Lock()
	if s.child != nil && s.child.Process != nil {
		_ = s.child.Process.Kill()
	}
	s.child = nil
	s.mu.Unlock()
	_ = s.client.Set(ctx, "triovexa:fault", "healthy", 0).Err()
	return s.StartWorker(ctx)
}

func (s *Supervisor) Handler(ctx context.Context) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]any{"status": "ok"}) })
	mux.HandleFunc("/state", func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			http.Error(w, "unauthorized", 401)
			return
		}
		backlog, _ := s.client.XLen(r.Context(), stream).Result()
		processed, _ := s.client.Get(r.Context(), "triovexa:stats:processed").Int64()
		produced, _ := s.client.Get(r.Context(), "triovexa:stats:produced").Int64()
		failures, _ := s.client.Get(r.Context(), "triovexa:stats:errors").Int64()
		heartbeat, _ := s.client.Get(r.Context(), "triovexa:worker:heartbeat").Result()
		paused, _ := s.client.Get(r.Context(), "triovexa:consumer:paused").Bool()
		fresh := false
		if parsed, err := time.Parse(time.RFC3339Nano, heartbeat); err == nil {
			fresh = time.Since(parsed) < 10*time.Second
		}
		writeJSON(w, 200, map[string]any{"target": "queue-worker", "source": "redis-streams", "timestamp": time.Now().UTC(), "complete": true, "worker_healthy": fresh, "consumer_paused": paused, "queue_backlog": backlog, "jobs_processed": processed, "jobs_produced": produced, "errors": failures, "generation": s.generation.Load()})
	})
	mux.HandleFunc("/operations", func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			http.Error(w, "unauthorized", 401)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		var request struct{ OperationID, Operation, Target, RequestedBy string }
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&request) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		if request.OperationID == "" || request.Target != "queue-worker" {
			http.Error(w, "operation_id and allowed target are required", 400)
			return
		}
		key := "triovexa:operation:" + request.OperationID
		if existing, _ := s.client.Get(r.Context(), key).Result(); existing != "" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(existing))
			return
		}
		var err error
		switch request.Operation {
		case "restart_worker":
			err = s.restart(ctx)
		case "pause_consumer":
			err = s.client.Set(r.Context(), "triovexa:consumer:paused", "true", 0).Err()
		case "resume_consumer":
			err = s.client.Set(r.Context(), "triovexa:consumer:paused", "false", 0).Err()
		default:
			http.Error(w, "unsupported operation", 400)
			return
		}
		status := "succeeded"
		statusCode := http.StatusOK
		if err != nil {
			status = "failed"
			statusCode = http.StatusInternalServerError
		}
		result, _ := json.Marshal(map[string]any{"operation_id": request.OperationID, "operation": request.Operation, "target": request.Target, "status": status, "finished_at": time.Now().UTC(), "error": errorText(err)})
		_ = s.client.Set(r.Context(), key, result, 24*time.Hour).Err()
		writeRawJSON(w, statusCode, result)
	})
	mux.HandleFunc("/faults", func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			http.Error(w, "unauthorized", 401)
			return
		}
		var request struct{ Mode string }
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.Mode != "healthy" && request.Mode != "stall" && request.Mode != "fail" {
			http.Error(w, "invalid fault mode", 400)
			return
		}
		_ = s.client.Set(r.Context(), "triovexa:fault", request.Mode, 0).Err()
		writeJSON(w, 200, map[string]any{"mode": request.Mode})
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		backlog, _ := s.client.XLen(r.Context(), stream).Result()
		processed, _ := s.client.Get(r.Context(), "triovexa:stats:processed").Int64()
		failures, _ := s.client.Get(r.Context(), "triovexa:stats:errors").Int64()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "triovexa_queue_backlog %d\ntriovexa_jobs_processed_total %d\ntriovexa_worker_errors_total %d\n", backlog, processed, failures)
	})
	return mux
}

func (s *Supervisor) authorized(r *http.Request) bool {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if s.token == "" || len(header) < 8 || !strings.EqualFold(header[:7], "Bearer ") {
		return false
	}
	provided := strings.TrimSpace(header[7:])
	return len(provided) == len(s.token) && subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) == 1
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	body, _ := json.Marshal(value)
	writeRawJSON(w, status, body)
}
func writeRawJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
func DurationEnv(key string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return value
}
func PortEnv(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if _, err := strconv.Atoi(value); err != nil {
		return fallback
	}
	return value
}
