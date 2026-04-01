package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/demo"
	"github.com/Cyaside/Triovexa/internal/domain"
)

type DemoCollector struct {
	baseURL string
	client  *http.Client
}

func NewDemoCollector(baseURL string) *DemoCollector {
	return &DemoCollector{
		baseURL: strings.TrimRight(baseURL, "/"),
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

func (c *DemoCollector) Collect(ctx context.Context, incident domain.Incident) ([]domain.EvidenceItem, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/state", nil)
	if err != nil {
		return nil, fmt.Errorf("create demo state request: %w", err)
	}

	response, err := c.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("query demo state: %w", err)
	}
	defer response.Body.Close()

	var snapshot demo.Snapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("decode demo state: %w", err)
	}

	observedAt := time.Now().UTC()
	return []domain.EvidenceItem{
		newEvidenceItem(incident.ID, "metric", "demo-service", fmt.Sprintf("error_rate=%.2f latency_ms=%d queue_backlog=%d replica_count=%d consumer_paused=%t", snapshot.ErrorRate, snapshot.LatencyMs, snapshot.QueueBacklog, snapshot.ReplicaCount, snapshot.ConsumerPaused), observedAt, map[string]any{
			"mode":            snapshot.Mode,
			"error_rate":      snapshot.ErrorRate,
			"latency_ms":      snapshot.LatencyMs,
			"queue_backlog":   snapshot.QueueBacklog,
			"replica_count":   snapshot.ReplicaCount,
			"consumer_paused": snapshot.ConsumerPaused,
		}),
		newEvidenceItem(incident.ID, "log", "demo-service", synthesizeLogLine(snapshot), observedAt, map[string]any{
			"mode":          snapshot.Mode,
			"workerHealthy": snapshot.WorkerHealthy,
		}),
		newEvidenceItem(incident.ID, "deploy", "demo-service", fmt.Sprintf("last_deploy=%s", snapshot.LastDeploy), observedAt, map[string]any{
			"last_deploy": snapshot.LastDeploy,
		}),
		newEvidenceItem(incident.ID, "service_metadata", "local-catalog", fmt.Sprintf("service=%s owner=platform-demo runtime=go", incident.ServiceName), observedAt, map[string]any{
			"owner":   "platform-demo",
			"runtime": "go",
		}),
	}, nil
}

func synthesizeLogLine(snapshot demo.Snapshot) string {
	switch snapshot.Mode {
	case demo.ModeWorkerStall:
		return "worker stalled while queue backlog kept growing; retry loop is not consuming new jobs"
	case demo.ModeTimeoutAfterDeploy:
		return "request timeouts increased shortly after deploy; downstream dependency appears slower than expected"
	case demo.ModeErrorRateSpike:
		return "error rate spike detected on primary request path with elevated latency"
	default:
		return "service health looks stable; no significant anomaly detected in latest snapshot"
	}
}

func newEvidenceItem(incidentID, itemType, source, snippet string, timestamp time.Time, metadata map[string]any) domain.EvidenceItem {
	return domain.EvidenceItem{
		ID:           uuid.NewString(),
		IncidentID:   incidentID,
		Type:         itemType,
		Source:       source,
		Snippet:      snippet,
		Timestamp:    timestamp,
		MetadataJSON: mustMarshal(metadata),
	}
}

func mustMarshal(payload map[string]any) string {
	body, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}

	return string(body)
}
