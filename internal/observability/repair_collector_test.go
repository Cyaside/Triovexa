package observability

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	repairverify "github.com/Cyaside/Triovexa/internal/coderepair/verification"
	"github.com/Cyaside/Triovexa/internal/domain"
)

type boundRepository struct{ binding coderepair.RepositoryBinding }

func (b boundRepository) GetActiveRepositoryBinding(context.Context, string, string) (coderepair.RepositoryBinding, error) {
	return b.binding, nil
}

type collectorFunc func(context.Context, domain.Incident) ([]domain.EvidenceItem, error)

func (f collectorFunc) Collect(ctx context.Context, i domain.Incident) ([]domain.EvidenceItem, error) {
	return f(ctx, i)
}

type collectedEvidence []domain.EvidenceItem

func (e collectedEvidence) ListEvidenceItems(context.Context, string) ([]domain.EvidenceItem, error) {
	return e, nil
}

func TestHTTPRepairCollectorUsesBoundPrometheusAndLogsWithoutQueueControl(t *testing.T) {
	p := &coderepair.TelemetryProfile{Version: coderepair.TelemetryVersion, Kind: "http", AlertName: "HTTPServerErrors", LogSources: []string{"grafana-loki"}, HTTP: &coderepair.HTTPSignals{RequestsMetric: "requests_total", HealthMetric: "health", RevisionMetric: "build_info", RevisionLabel: "revision", StatusLabel: "status", ServiceLabel: "service", EnvironmentLabel: "environment", WindowSeconds: 30, MinimumRequests: 1, MaxErrorRatio: 0.01}}
	binding := coderepair.RepositoryBinding{ID: "binding", ServiceName: "api", Environment: "staging", RepositoryURL: "https://github.com/acme/api", BaseRef: "main", AllowedPaths: []string{"src"}, TestRecipes: []string{"test-api"}, TelemetryProfile: p, PolicyVersion: "repair-v3", Enabled: true}
	queries, _ := repairverify.RenderHTTPQueries(*binding.TelemetryContract())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := time.Now().UTC()
		labels := map[string]string{"service": "api", "environment": "staging"}
		var body any
		switch r.URL.Path {
		case "/api/v1/query":
			value := "1"
			switch r.URL.Query().Get("query") {
			case queries.Requests:
				labels["status"] = "500"
				value = "100"
			case queries.Health:
				value = "0"
			case queries.Revision:
				labels["revision"] = strings.Repeat("1", 40)
			default:
				t.Errorf("unexpected query %s", r.URL.RawQuery)
				w.WriteHeader(400)
				return
			}
			body = map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": []any{map[string]any{"metric": labels, "value": []any{float64(now.UnixNano()) / 1e9, value}}}}}
		case "/api/v1/rules":
			body = map[string]any{"status": "success", "data": map[string]any{"groups": []any{map[string]any{"rules": []any{map[string]any{"name": "HTTPServerErrors", "type": "alerting", "health": "ok", "lastEvaluation": now}}}}}}
		case "/api/v1/alerts":
			body = map[string]any{"status": "success", "data": map[string]any{"alerts": []any{map[string]any{"labels": map[string]string{"alertname": "HTTPServerErrors", "service": "api", "environment": "staging"}, "state": "firing"}}}}
		default:
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer server.Close()
	next := collectorFunc(func(context.Context, domain.Incident) ([]domain.EvidenceItem, error) {
		t.Error("HTTP profile used local queue collector")
		return nil, errors.New("queue unavailable")
	})
	logs := collectorFunc(func(_ context.Context, i domain.Incident) ([]domain.EvidenceItem, error) {
		return []domain.EvidenceItem{{ID: "log", IncidentID: i.ID, Type: "log", Source: "grafana-loki", Snippet: "RuntimeError in health handler", Timestamp: time.Now().UTC()}}, nil
	})
	incident := domain.Incident{ID: "incident", ServiceName: "api", Environment: "staging", State: domain.IncidentStateEscalated}
	collector := NewRepairCollector(boundRepository{binding}, next, logs, repairverify.ClientConfig{PrometheusURL: server.URL})
	items, err := collector.Collect(t.Context(), incident)
	if err != nil || len(items) != 2 {
		t.Fatalf("HTTP collector: %d %v", len(items), err)
	}
	snapshot, err := coderepair.CaptureEvidenceForBinding(t.Context(), collectedEvidence(items), incident, binding, time.Now().UTC(), coderepair.EvidenceLimits{MaxItems: 10, MaxSnippetBytes: 1024, MaxTotalBytes: 2048, MaxAge: time.Minute})
	if err != nil || coderepair.CheckInvestigationEvidence(incident, binding, snapshot, time.Now().UTC()) != nil || snapshot.DeployedRevision != strings.Repeat("1", 40) {
		t.Fatalf("collected evidence could not authorize scoped HTTP investigation: %v", err)
	}
}
