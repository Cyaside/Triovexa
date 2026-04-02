package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/domain"
)

func TestGrafanaClientPrometheusInstantValue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("Authorization header = %q, want bearer token", got)
		}
		if r.URL.Path != "/api/datasources/proxy/uid/prom/api/v1/query" {
			t.Fatalf("Path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"value":[1710000000,"0.42"]}]}}`))
	}))
	defer server.Close()

	client := NewGrafanaClient(server.URL, "token", "prom", "logs")
	value, err := client.PrometheusInstantValue(context.Background(), "up", time.Unix(1710000000, 0))
	if err != nil {
		t.Fatalf("PrometheusInstantValue returned error: %v", err)
	}
	if value != 0.42 {
		t.Fatalf("PrometheusInstantValue = %v, want %v", value, 0.42)
	}
}

func TestGrafanaClientLokiLines(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/datasources/proxy/uid/logs/loki/api/v1/query_range" {
			t.Fatalf("Path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"values":[["1710000000000000000","deploy finished v1.2.3"],["1710000001000000000","timeout increasing"]]}]}}`))
	}))
	defer server.Close()

	client := NewGrafanaClient(server.URL, "token", "prom", "logs")
	lines, err := client.LokiLines(context.Background(), `{service="checkout-service"}`, time.Unix(1710000000, 0), time.Unix(1710000300, 0), 5)
	if err != nil {
		t.Fatalf("LokiLines returned error: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("len(lines) = %d, want %d", len(lines), 2)
	}
}

func TestRenderQueryTemplate(t *testing.T) {
	incident := domainFixture()
	rendered := renderQueryTemplate(`rate(http_requests_total{service="{{service}}",environment="{{environment}}"}[5m])`, incident)

	if rendered != `rate(http_requests_total{service="checkout-service",environment="staging"}[5m])` {
		t.Fatalf("renderQueryTemplate() = %q", rendered)
	}
}

func TestClassifyGrafanaSnapshot(t *testing.T) {
	if got := classifyGrafanaSnapshot(0.01, 200, 0); got != "healthy" {
		t.Fatalf("healthy classification = %q", got)
	}
	if got := classifyGrafanaSnapshot(0.20, 200, 0); got != "error_rate_spike" {
		t.Fatalf("error rate classification = %q", got)
	}
}

func domainFixture() domain.Incident {
	return domain.Incident{
		ServiceName: "checkout-service",
		Environment: "staging",
		Severity:    "critical",
		Title:       "checkout timeout after deploy",
	}
}
