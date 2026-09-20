package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/telemetry"
)

func TestSecurityMiddlewareRejectsCrossOriginMutation(t *testing.T) {
	handler := securityMiddleware(config.Config{}, nil, telemetry.NewRecorder(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodPost, "http://triovexa.test/api/v1/actions/1/approve", nil)
	request.Header.Set("Origin", "https://attacker.test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.Code)
	}
}

func TestSecurityMiddlewareAcceptsSameOriginMutation(t *testing.T) {
	handler := securityMiddleware(config.Config{}, nil, telemetry.NewRecorder(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodPost, "http://triovexa.test/api/v1/actions/1/approve", nil)
	request.Header.Set("Origin", "http://triovexa.test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", response.Code)
	}
}

func TestWebhookRateLimitRejectsAndRecordsMetric(t *testing.T) {
	recorder := telemetry.NewRecorder()
	handler := securityMiddleware(config.Config{WebhookRateLimit: 1}, nil, recorder, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	for attempt := 0; attempt < 2; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "http://triovexa.test/webhooks/grafana", strings.NewReader(`{}`))
		request.RemoteAddr = "192.0.2.1:1234"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if attempt == 1 && response.Code != http.StatusTooManyRequests {
			t.Fatalf("second status = %d, want 429", response.Code)
		}
	}
	metrics := httptest.NewRecorder()
	recorder.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "triovexa_webhook_rate_limited_total 1") {
		t.Fatalf("rate limit metric missing: %s", metrics.Body.String())
	}
}
