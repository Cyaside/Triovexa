package http

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/storage"
)

func TestIncidentDetailAPIReturnsEmptyArraysInsteadOfNull(t *testing.T) {
	repository := storage.NewMemoryStore()
	incident := domain.Incident{ID: "incident-empty", State: domain.IncidentStateDetected, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := repository.CreateIncident(context.Background(), incident); err != nil {
		t.Fatal(err)
	}
	server := NewServer(config.Config{ServiceName: "test", HTTPHost: "127.0.0.1", HTTPPort: "0"}, slog.New(slog.NewTextHandler(io.Discard, nil)), repository, nil, nil, nil)
	api := httptest.NewServer(server.Handler)
	defer api.Close()
	response, err := stdhttp.Get(api.URL + "/api/v1/incidents/" + incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"evidence", "documents", "candidate_actions", "policy_decisions", "approval_records", "execution_records", "verification_results", "rollback_records", "audit_events"} {
		value, ok := payload[key].([]any)
		if !ok || len(value) != 0 {
			t.Fatalf("%s = %#v, want empty array", key, payload[key])
		}
	}
}

func TestConnectionsExposeAndProbeConfiguredMonitoringServices(t *testing.T) {
	ready := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if r.URL.Path != "/-/ready" {
			stdhttp.NotFound(w, r)
			return
		}
		w.WriteHeader(stdhttp.StatusOK)
	}))
	defer ready.Close()

	repository := storage.NewMemoryStore()
	server := NewServer(config.Config{
		ServiceName:         "test",
		HTTPHost:            "127.0.0.1",
		HTTPPort:            "0",
		DeploymentMode:      "local-demo",
		PrometheusBaseURL:   ready.URL,
		AlertmanagerBaseURL: ready.URL,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), repository, nil, nil, nil)
	apiServer := httptest.NewServer(server.Handler)
	defer apiServer.Close()

	response, err := stdhttp.Get(apiServer.URL + "/api/v1/connections")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload map[string]map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"prometheus", "alertmanager"} {
		if payload[name]["configured"] != true || payload[name]["endpoint"] != ready.URL {
			t.Fatalf("%s status = %#v", name, payload[name])
		}
		probe, err := stdhttp.Post(apiServer.URL+"/api/v1/connections/"+name+"/test", "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		if probe.StatusCode != stdhttp.StatusOK {
			probe.Body.Close()
			t.Fatalf("%s probe status = %d", name, probe.StatusCode)
		}
		probe.Body.Close()
	}
}

func TestReadinessProbeRejectsRedirects(t *testing.T) {
	target := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		w.WriteHeader(stdhttp.StatusOK)
	}))
	defer target.Close()
	redirect := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		stdhttp.Redirect(w, r, target.URL, stdhttp.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	if err := probeReadiness(context.Background(), redirect.URL); err == nil {
		t.Fatal("probeReadiness followed a redirect")
	}
}
