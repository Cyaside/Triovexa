package http

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/readiness"
	"github.com/Cyaside/Triovexa/internal/storage"
)

type staticReadiness struct{ report readiness.Report }

func (s staticReadiness) Check(context.Context) readiness.Report { return s.report }

func TestHealthReturnsDependencySpecificDegradedStatus(t *testing.T) {
	repository := storage.NewMemoryStore()
	server := NewServer(config.Config{HTTPHost: "127.0.0.1", HTTPPort: "0"}, slog.New(slog.NewTextHandler(io.Discard, nil)), repository, nil, nil, nil, &RuntimeControls{
		Readiness: staticReadiness{report: readiness.Report{Status: "degraded", Dependencies: map[string]readiness.Dependency{
			"postgresql": {Status: "ok"}, "redis": {Status: "failed", Error: "dependency check failed"}, "supervisor": {Status: "ok"},
		}}},
	})
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
	if body := response.Body.String(); body == "" || !containsAll(body, `"redis"`, `"failed"`, `"postgresql"`, `"supervisor"`) {
		t.Fatalf("dependency report missing: %s", body)
	}
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		found := false
		for i := 0; i+len(part) <= len(value); i++ {
			if value[i:i+len(part)] == part {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
