package readiness

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type failingPinger struct{}

func (failingPinger) Ping(context.Context) error {
	return errors.New("database unavailable with password=secret")
}

func TestCheckerReportsSpecificDependencyFailure(t *testing.T) {
	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer supervisor.Close()
	checker := NewChecker(failingPinger{}, "", supervisor.URL)
	report := checker.Check(context.Background())
	if report.Status != "degraded" || report.Dependencies["postgresql"].Status != "failed" {
		t.Fatalf("unexpected report: %#v", report)
	}
	if report.Dependencies["postgresql"].Error != "dependency check failed" {
		t.Fatalf("raw dependency error leaked: %#v", report.Dependencies["postgresql"])
	}
	if report.Dependencies["supervisor"].Status != "ok" {
		t.Fatalf("supervisor status = %#v", report.Dependencies["supervisor"])
	}
}
