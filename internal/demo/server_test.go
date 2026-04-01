package demo

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDemoIncidentSimulation(t *testing.T) {
	t.Parallel()

	server := NewServer(Config{Address: ":0"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/simulate/worker-stall", nil)

	server.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusAccepted)
	}

	healthRecorder := httptest.NewRecorder()
	healthRequest := httptest.NewRequest(http.MethodGet, "/health", nil)
	server.Handler.ServeHTTP(healthRecorder, healthRequest)

	if healthRecorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("health status = %d, want %d", healthRecorder.Code, http.StatusServiceUnavailable)
	}
}
