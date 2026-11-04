package verification

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

func httpContract() *coderepair.TelemetryContract {
	return &coderepair.TelemetryContract{ServiceName: "api", Environment: "staging", Profile: coderepair.TelemetryProfile{
		Version: coderepair.TelemetryVersion, Kind: "http", AlertName: "HTTPServerErrors", LogSources: []string{"grafana-loki"},
		HTTP: &coderepair.HTTPSignals{RequestsMetric: "http_requests_total", HealthMetric: "probe_success", RevisionMetric: "app_build_info", RevisionLabel: "revision", StatusLabel: "status", ServiceLabel: "service", EnvironmentLabel: "environment", RequestLabels: map[string]string{"path": "/healthz"}, WindowSeconds: 30, MinimumRequests: 1, MaxErrorRatio: 0.01},
	}}
}

func TestHTTPProfileCollectsScopedRevisionAndVerifiesRecovery(t *testing.T) {
	contract := httpContract()
	queries, err := RenderHTTPQueries(*contract)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{queries.Requests, queries.Health, queries.Revision} {
		for _, part := range []string{`service="api"`, `environment="staging"`, "timestamp(", "<= 60", ">= -5"} {
			if !strings.Contains(query, part) {
				t.Fatalf("query lacks identity or freshness %q: %s", part, query)
			}
		}
	}
	var recovered bool
	var broken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := time.Now().UTC()
		labels := map[string]string{"service": "api", "environment": "staging"}
		if broken == "wrong target" {
			labels["service"] = "another-api"
		}
		var body any
		switch r.URL.Path {
		case "/api/v1/query":
			value := 100.0
			switch r.URL.Query().Get("query") {
			case queries.Requests:
				labels["status"] = "500"
				if recovered {
					labels["status"] = "200"
				}
				if broken == "invalid status" {
					labels["status"] = "unknown"
				}
			case queries.Health:
				value = 0
				if recovered {
					value = 1
				}
			case queries.Revision:
				value = 1
				labels["revision"] = strings.Repeat("1", 40)
				if recovered {
					labels["revision"] = strings.Repeat("2", 40)
				}
				if broken == "invalid revision" {
					labels["revision"] = "main"
				}
			default:
				t.Errorf("unregistered query: %s", r.URL.RawQuery)
				w.WriteHeader(400)
				return
			}
			if broken == "missing revision instance" && r.URL.Query().Get("query") == queries.Health {
				labels["instance"] = "pod-a"
			}
			at := now
			if broken == "stale" {
				at = now.Add(-2 * time.Minute)
			}
			points := []any{map[string]any{"metric": labels, "value": []any{float64(at.UnixNano()) / 1e9, valueString(value)}}}
			if broken == "missing" {
				points = nil
			}
			if broken == "mixed revision" && r.URL.Query().Get("query") == queries.Revision {
				other := map[string]string{"service": "api", "environment": "staging", "revision": strings.Repeat("3", 40)}
				points = append(points, map[string]any{"metric": other, "value": []any{float64(now.UnixNano()) / 1e9, "1"}})
			}
			body = map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": points}}
		case "/api/v1/rules":
			rules := []any{map[string]any{"name": "HTTPServerErrors", "type": "alerting", "health": "ok", "lastEvaluation": now}}
			if broken == "missing rule" {
				rules = nil
			}
			if broken == "ambiguous rule" {
				rules = append(rules, rules[0])
			}
			body = map[string]any{"status": "success", "data": map[string]any{"groups": []any{map[string]any{"rules": rules}}}}
		case "/api/v1/alerts":
			alerts := []any{}
			if broken == "missing alerts" {
				alerts = nil
			}
			if broken == "unscoped alert" {
				alerts = append(alerts, map[string]any{"labels": map[string]string{"alertname": "HTTPServerErrors"}, "state": "firing"})
			}
			if !recovered {
				alerts = append(alerts, map[string]any{"labels": map[string]string{"alertname": "HTTPServerErrors", "service": "api", "environment": "staging"}, "state": "firing"})
			}
			body = map[string]any{"status": "success", "data": map[string]any{"alerts": alerts}}
		default:
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer server.Close()
	client, err := NewClient(ClientConfig{PrometheusURL: server.URL}, contract)
	if err != nil {
		t.Fatal(err)
	}
	before, err := client.Snapshot(t.Context())
	if err != nil || before.HTTP == nil || before.HTTP.Errors != 100 || before.AlertCleared {
		t.Fatalf("bad baseline: %+v %v", before, err)
	}
	recovered = true
	after, err := client.Snapshot(t.Context())
	if err != nil || !CheckFor(contract, before, after, strings.Repeat("2", 40)) {
		t.Fatalf("HTTP recovery rejected: %+v %v", after, err)
	}
	for _, scenario := range []string{"wrong target", "invalid status", "invalid revision", "missing", "stale", "mixed revision", "missing rule", "ambiguous rule", "missing revision instance", "missing alerts", "unscoped alert"} {
		t.Run(scenario, func(t *testing.T) {
			broken = scenario
			if _, err := client.Snapshot(t.Context()); err == nil {
				t.Fatal("incomplete/foreign observation accepted")
			}
		})
	}
	broken = ""
	for _, change := range []struct {
		name   string
		mutate func(*Sample)
	}{
		{"no traffic", func(s *Sample) { s.HTTP.Requests = 0 }},
		{"errors remain", func(s *Sample) { s.HTTP.Errors = s.HTTP.Requests }},
		{"wrong profile", func(s *Sample) { s.ProfileSHA256 = strings.Repeat("a", 64) }},
		{"wrong environment", func(s *Sample) { s.Environment = "production" }},
		{"wrong revision", func(s *Sample) { s.DeployedRevision = before.DeployedRevision }},
		{"alert firing", func(s *Sample) { s.AlertCleared = false }},
	} {
		t.Run(change.name, func(t *testing.T) {
			copy := after
			h := *after.HTTP
			copy.HTTP = &h
			change.mutate(&copy)
			if CheckFor(contract, before, copy, strings.Repeat("2", 40)) {
				t.Fatal("false HTTP recovery")
			}
		})
	}
}

func valueString(v float64) string {
	if v == 0 {
		return "0"
	}
	if v == 1 {
		return "1"
	}
	return "100"
}

func TestHTTPQueryRendererQuotesLiteralLabelsAndRejectsExpressions(t *testing.T) {
	c := httpContract()
	c.Profile.HTTP.RequestLabels["path"] = `/x"} or vector(1) #`
	q, err := RenderHTTPQueries(*c)
	if err != nil || !strings.Contains(q.Requests, `path="/x\"} or vector(1) #"`) {
		t.Fatalf("literal labels not quoted: %s %v", q.Requests, err)
	}
	c.Profile.HTTP.RequestsMetric = `http_requests_total{} or vector(1)`
	if _, err := RenderHTTPQueries(*c); err == nil {
		t.Fatal("query expression accepted as a metric name")
	}
}
