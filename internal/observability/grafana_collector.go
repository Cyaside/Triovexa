package observability

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Cyaside/Triovexa/internal/demo"
	"github.com/Cyaside/Triovexa/internal/domain"
)

type GrafanaSignalConfig struct {
	ErrorRateQuery  string
	LatencyQuery    string
	QueueQuery      string
	ReplicaQuery    string
	LogsQuery       string
	DeployLogsQuery string
	Lookback        time.Duration
}

type GrafanaCollector struct {
	client   *GrafanaClient
	signals  GrafanaSignalConfig
	renderer *QueryRenderer
}

func NewGrafanaCollector(client *GrafanaClient, signals GrafanaSignalConfig, renderers ...*QueryRenderer) *GrafanaCollector {
	renderer := NewQueryRenderer()
	if len(renderers) > 0 && renderers[0] != nil {
		renderer = renderers[0]
	}
	return &GrafanaCollector{client: client, signals: signals, renderer: renderer}
}

func (c *GrafanaCollector) Collect(ctx context.Context, incident domain.Incident) ([]domain.EvidenceItem, error) {
	if c.client == nil || !c.client.Configured() {
		return []domain.EvidenceItem{
			newEvidenceItem(incident.ID, "service_metadata", "grafana", "grafana collector is selected but not fully configured", time.Now().UTC(), map[string]any{
				"service": incident.ServiceName,
				"mode":    "grafana",
			}),
		}, nil
	}

	observedAt := time.Now().UTC()
	metricValues := map[string]any{}
	var metricParts []string
	var items []domain.EvidenceItem

	for _, query := range []struct {
		key      string
		template string
		label    string
	}{
		{key: "error_rate", template: c.signals.ErrorRateQuery, label: "error_rate"},
		{key: "latency_ms", template: c.signals.LatencyQuery, label: "latency_ms"},
		{key: "queue_backlog", template: c.signals.QueueQuery, label: "queue_backlog"},
		{key: "replica_count", template: c.signals.ReplicaQuery, label: "replica_count"},
	} {
		rendered := c.renderer.Render(query.template, incident)
		if rendered == "" {
			continue
		}

		value, err := c.client.PrometheusInstantValue(ctx, rendered, observedAt)
		if err != nil {
			continue
		}

		metricValues[query.key] = value
		metricParts = append(metricParts, fmt.Sprintf("%s=%.2f", query.label, value))
	}

	sort.Strings(metricParts)
	if len(metricParts) > 0 {
		items = append(items, newEvidenceItem(incident.ID, "metric", "grafana-prometheus", strings.Join(metricParts, " "), observedAt, metricValues))
	}

	lookback := c.signals.Lookback
	if lookback <= 0 {
		lookback = 15 * time.Minute
	}

	if rendered := c.renderer.Render(c.signals.LogsQuery, incident); rendered != "" {
		lines, err := c.client.LokiLines(ctx, rendered, observedAt.Add(-lookback), observedAt, 5)
		if err == nil && len(lines) > 0 {
			items = append(items, newEvidenceItem(incident.ID, "log", "grafana-loki", lines[0], observedAt, map[string]any{
				"log_count": len(lines),
				"lines":     lines,
			}))
		}
	}

	if rendered := c.renderer.Render(c.signals.DeployLogsQuery, incident); rendered != "" {
		lines, err := c.client.LokiLines(ctx, rendered, observedAt.Add(-lookback), observedAt, 5)
		if err == nil && len(lines) > 0 {
			items = append(items, newEvidenceItem(incident.ID, "deploy", "grafana-loki", lines[0], observedAt, map[string]any{
				"last_deploy": extractDeploymentVersion(lines),
				"lines":       lines,
			}))
		}
	}

	items = append(items, newEvidenceItem(incident.ID, "service_metadata", "grafana", fmt.Sprintf("service=%s environment=%s severity=%s", incident.ServiceName, incident.Environment, incident.Severity), observedAt, map[string]any{
		"mode":        "grafana",
		"service":     incident.ServiceName,
		"environment": incident.Environment,
		"severity":    incident.Severity,
	}))

	return items, nil
}

type GrafanaSnapshotFetcher struct {
	client   *GrafanaClient
	signals  GrafanaSignalConfig
	renderer *QueryRenderer
}

func NewGrafanaSnapshotFetcher(client *GrafanaClient, signals GrafanaSignalConfig, renderers ...*QueryRenderer) *GrafanaSnapshotFetcher {
	renderer := NewQueryRenderer()
	if len(renderers) > 0 && renderers[0] != nil {
		renderer = renderers[0]
	}
	return &GrafanaSnapshotFetcher{client: client, signals: signals, renderer: renderer}
}

func (f *GrafanaSnapshotFetcher) Snapshot(ctx context.Context) (demo.Snapshot, error) {
	return f.SnapshotForIncident(ctx, domain.Incident{})
}

func (f *GrafanaSnapshotFetcher) SnapshotForIncident(ctx context.Context, incident domain.Incident) (demo.Snapshot, error) {
	if f.client == nil || !f.client.Configured() {
		return demo.Snapshot{}, fmt.Errorf("grafana snapshot fetcher is not configured")
	}

	observedAt := time.Now().UTC()
	errorRate, err := f.client.PrometheusInstantValue(ctx, f.renderer.Render(f.signals.ErrorRateQuery, incident), observedAt)
	if err != nil {
		return demo.Snapshot{}, err
	}

	latencyMs, err := f.client.PrometheusInstantValue(ctx, f.renderer.Render(f.signals.LatencyQuery, incident), observedAt)
	if err != nil {
		return demo.Snapshot{}, err
	}

	queueBacklog, err := f.client.PrometheusInstantValue(ctx, f.renderer.Render(f.signals.QueueQuery, incident), observedAt)
	if err != nil {
		return demo.Snapshot{}, err
	}

	replicaCount := 0
	if rendered := f.renderer.Render(f.signals.ReplicaQuery, incident); rendered != "" {
		value, err := f.client.PrometheusInstantValue(ctx, rendered, observedAt)
		if err == nil {
			replicaCount = int(value)
		}
	}

	snapshot := demo.Snapshot{
		Mode:           classifyGrafanaSnapshot(errorRate, latencyMs, queueBacklog),
		ErrorRate:      errorRate,
		LatencyMs:      int(latencyMs),
		QueueBacklog:   int(queueBacklog),
		ReplicaCount:   replicaCount,
		ConsumerPaused: false,
		WorkerHealthy:  errorRate < 0.05 && latencyMs < 1000 && queueBacklog < 50,
		LastUpdatedUTC: observedAt,
	}

	if rendered := f.renderer.Render(f.signals.DeployLogsQuery, incident); rendered != "" {
		lines, err := f.client.LokiLines(ctx, rendered, observedAt.Add(-f.lookback()), observedAt, 3)
		if err == nil && len(lines) > 0 {
			snapshot.LastDeploy = extractDeploymentVersion(lines)
		}
	}
	if snapshot.LastDeploy == "" {
		snapshot.LastDeploy = "unknown"
	}

	return snapshot, nil
}

func (f *GrafanaSnapshotFetcher) lookback() time.Duration {
	if f.signals.Lookback <= 0 {
		return 15 * time.Minute
	}
	return f.signals.Lookback
}

func classifyGrafanaSnapshot(errorRate float64, latencyMs float64, queueBacklog float64) demo.Mode {
	switch {
	case queueBacklog >= 100:
		return demo.ModeWorkerStall
	case latencyMs >= 1000:
		return demo.ModeTimeoutAfterDeploy
	case errorRate >= 0.1:
		return demo.ModeErrorRateSpike
	default:
		return demo.ModeHealthy
	}
}

var deployVersionPattern = regexp.MustCompile(`v[0-9]+(?:\.[0-9]+){1,3}`)

func extractDeploymentVersion(lines []string) string {
	for _, line := range lines {
		match := deployVersionPattern.FindString(line)
		if match != "" {
			return match
		}
	}

	return "unknown"
}
