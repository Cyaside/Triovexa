package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/Cyaside/Triovexa/internal/alerting"
	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/incident"
	"github.com/Cyaside/Triovexa/internal/storage"
)

func TestGrafanaWebhookProcessesEveryAlert(t *testing.T) {
	repository := storage.NewMemoryStore()
	service := incident.NewService(repository, nil, nil, nil, nil, nil)
	server := NewServer(config.Config{ServiceName: "test"}, slog.New(slog.NewTextHandler(io.Discard, nil)), repository, service, nil, nil)
	api := httptest.NewServer(server.Handler)
	defer api.Close()
	payload := alerting.GrafanaWebhookPayload{Title: "group", State: "firing", Alerts: []alerting.GrafanaAlert{
		{Status: "firing", Fingerprint: "a", Labels: map[string]string{"service": "worker-a"}},
		{Status: "firing", Fingerprint: "b", Labels: map[string]string{"service": "worker-b"}},
	}}
	body, _ := json.Marshal(payload)
	response, err := stdhttp.Post(api.URL+"/webhooks/grafana", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	incidents, err := repository.ListIncidents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(incidents) != 2 {
		t.Fatalf("incident count = %d, want 2", len(incidents))
	}
}
