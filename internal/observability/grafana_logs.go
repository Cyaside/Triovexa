package observability

import (
	"context"
	"errors"
	"time"

	"github.com/Cyaside/Triovexa/internal/domain"
)

type repairLogs struct{ grafana *GrafanaCollector }

func NewRepairLogCollector(grafana *GrafanaCollector) IncidentCollector {
	return &repairLogs{grafana: grafana}
}

func (c *repairLogs) Collect(ctx context.Context, incident domain.Incident) ([]domain.EvidenceItem, error) {
	if c.grafana == nil {
		return nil, errors.New("repair log collector is not configured")
	}
	return c.grafana.applicationLogs(ctx, incident, time.Now().UTC(), time.Minute)
}

func (c *GrafanaCollector) applicationLogs(ctx context.Context, incident domain.Incident, observedAt time.Time, lookback time.Duration) ([]domain.EvidenceItem, error) {
	if c.client == nil || !c.client.Configured() {
		return nil, errors.New("Grafana repair logs are not configured")
	}
	query := c.renderer.Render(c.signals.LogsQuery, incident)
	if query == "" {
		return nil, errors.New("repair log query is not configured")
	}
	lines, err := c.client.LokiLines(ctx, query, observedAt.Add(-lookback), observedAt, 5)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, errors.New("no recent application logs are available")
	}
	return []domain.EvidenceItem{newEvidenceItem(incident.ID, "log", "grafana-loki", lines[0], observedAt, map[string]any{"log_count": len(lines), "lines": lines})}, nil
}
