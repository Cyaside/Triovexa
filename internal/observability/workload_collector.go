package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/domain"
)

type WorkloadCollector struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewWorkloadCollector(baseURL, token string) *WorkloadCollector {
	return &WorkloadCollector{baseURL: strings.TrimRight(baseURL, "/"), token: strings.TrimSpace(token), client: &http.Client{Timeout: 5 * time.Second}}
}

func (c *WorkloadCollector) Collect(ctx context.Context, incident domain.Incident) ([]domain.EvidenceItem, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/state", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request workload evidence: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("workload evidence returned %d", response.StatusCode)
	}
	var state struct {
		Target         string    `json:"target"`
		Source         string    `json:"source"`
		Timestamp      time.Time `json:"timestamp"`
		Complete       bool      `json:"complete"`
		WorkerHealthy  bool      `json:"worker_healthy"`
		ConsumerPaused bool      `json:"consumer_paused"`
		QueueBacklog   int       `json:"queue_backlog"`
		JobsProcessed  int64     `json:"jobs_processed"`
		JobsProduced   int64     `json:"jobs_produced"`
		Errors         int64     `json:"errors"`
		Generation     int64     `json:"generation"`
	}
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		return nil, fmt.Errorf("decode workload evidence: %w", err)
	}
	if !state.Complete || state.Timestamp.IsZero() || time.Since(state.Timestamp) > time.Minute {
		return nil, fmt.Errorf("workload evidence is incomplete or stale")
	}
	mode := "healthy"
	snippet := fmt.Sprintf("worker healthy; queue backlog=%d; processed=%d", state.QueueBacklog, state.JobsProcessed)
	if !state.WorkerHealthy || state.ConsumerPaused || state.QueueBacklog > 20 {
		mode = "worker_stall"
		snippet = fmt.Sprintf("worker stalled; queue backlog=%d; processed=%d; consumer_paused=%t", state.QueueBacklog, state.JobsProcessed, state.ConsumerPaused)
	}
	metadata, _ := json.Marshal(map[string]any{
		"target": state.Target, "source": state.Source, "complete": state.Complete,
		"mode": mode, "workerHealthy": state.WorkerHealthy, "consumer_paused": state.ConsumerPaused,
		"queue_backlog": state.QueueBacklog, "jobs_processed": state.JobsProcessed, "jobs_produced": state.JobsProduced,
		"error_rate": float64(state.Errors), "latency_ms": 0, "replica_count": 1, "generation": state.Generation,
	})
	return []domain.EvidenceItem{{
		ID: uuid.NewString(), IncidentID: incident.ID, Type: "metric", Source: "workload-control",
		Snippet: snippet, Timestamp: state.Timestamp, MetadataJSON: string(metadata),
	}}, nil
}
