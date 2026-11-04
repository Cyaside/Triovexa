package observability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	repairverify "github.com/Cyaside/Triovexa/internal/coderepair/verification"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/google/uuid"
)

type IncidentCollector interface {
	Collect(context.Context, domain.Incident) ([]domain.EvidenceItem, error)
}
type repairBindingReader interface {
	GetActiveRepositoryBinding(context.Context, string, string) (coderepair.RepositoryBinding, error)
}

type RepairCollector struct {
	bindings repairBindingReader
	next     IncidentCollector
	httpLogs IncidentCollector
	config   repairverify.ClientConfig
}

func NewRepairCollector(bindings repairBindingReader, next, httpLogs IncidentCollector, config repairverify.ClientConfig) *RepairCollector {
	return &RepairCollector{bindings: bindings, next: next, httpLogs: httpLogs, config: config}
}

func (c *RepairCollector) Collect(ctx context.Context, incident domain.Incident) ([]domain.EvidenceItem, error) {
	binding, err := c.bindings.GetActiveRepositoryBinding(ctx, incident.ServiceName, incident.Environment)
	if errors.Is(err, coderepair.ErrRepositoryBindingNotFound) {
		return c.next.Collect(ctx, incident)
	}
	if err != nil {
		return nil, err
	}
	if binding.TelemetryProfile == nil {
		return c.next.Collect(ctx, incident)
	} // Keep historical collector behavior explicit.
	if err := binding.Validate(); err != nil {
		return nil, err
	}
	selected := c.next
	if binding.TelemetryProfile.Kind == "http" {
		selected = c.httpLogs
	}
	if selected == nil {
		return nil, errors.New("HTTP evidence requires the configured log collector")
	}
	items, err := selected.Collect(ctx, incident)
	if err != nil {
		return nil, err
	}
	client, err := repairverify.NewClient(c.config, binding.TelemetryContract())
	if err != nil {
		return nil, err
	}
	sample, err := client.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	metadata, err := json.Marshal(sample)
	if err != nil {
		return nil, err
	}
	// Use one authoritative metric for this profile; do not mix local demo
	// queue state into another service's source-level evidence.
	filtered := make([]domain.EvidenceItem, 0, len(items)+1)
	for _, item := range items {
		if item.Source != "workload-control" && item.Source != binding.MetricEvidenceSource() {
			filtered = append(filtered, item)
		}
	}
	snippet := fmt.Sprintf("queue backlog=%d; processed=%d; healthy=%t", sample.QueueBacklog, sample.JobsProcessed, sample.WorkerHealthy)
	if sample.HTTP != nil {
		snippet = fmt.Sprintf("HTTP requests=%.3f; server_errors=%.3f; healthy=%t; alert_cleared=%t", sample.HTTP.Requests, sample.HTTP.Errors, sample.HTTP.Healthy, sample.AlertCleared)
	}
	filtered = append(filtered, domain.EvidenceItem{ID: uuid.NewString(), IncidentID: incident.ID, Type: "metric", Source: binding.MetricEvidenceSource(), Snippet: snippet, Timestamp: sample.Timestamp, MetadataJSON: string(metadata)})
	return filtered, nil
}
