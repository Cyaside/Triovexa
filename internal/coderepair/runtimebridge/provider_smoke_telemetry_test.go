//go:build provider_smoke

package runtimebridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/security"
	"github.com/google/uuid"
)

// This local smoke collector is a trusted host control boundary. It pins the
// running container's image/revision, then reads actual Prometheus and Docker
// logs. It is not a production Kubernetes/OpenSearch integration.
type repositorySmokeCollector struct{ cfg repositorySmokeConfig }

func (c *repositorySmokeCollector) Collect(ctx context.Context, i domain.Incident) ([]domain.EvidenceItem, error) {
	if i.ServiceName != c.cfg.Binding.ServiceName || i.Environment != c.cfg.Binding.Environment {
		return nil, errors.New("telemetry target mismatch")
	}
	return c.collect(ctx, i.ID)
}

func (c *repositorySmokeCollector) collect(ctx context.Context, incidentID string) ([]domain.EvidenceItem, error) {
	var container struct {
		Image  string
		State  struct{ Running bool }
		Config struct{ Labels map[string]string }
	}
	raw, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .}}", "--", c.cfg.ContainerName).Output()
	if err != nil || json.Unmarshal(raw, &container) != nil || !container.State.Running ||
		container.Config.Labels["org.opencontainers.image.revision"] != c.cfg.BaseSHA ||
		container.Config.Labels["triovexa.test"] != c.cfg.ValidationID {
		return nil, errors.New("trusted Docker deployment provenance is unavailable")
	}
	image, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", "--", c.cfg.Binding.ValidationProfile.Image).Output()
	if err != nil || strings.TrimSpace(string(image)) != container.Image {
		return nil, errors.New("deployed image differs from the approved test image")
	}
	query := `sum(intern_sample_api_requests_total{service=` + strconv.Quote(c.cfg.Binding.ServiceName) + `,path="/healthz",status="500"})`
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.PrometheusURL+"/api/v1/query?query="+url.QueryEscape(query), nil)
	if err != nil {
		return nil, err
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return nil, errors.New("real Prometheus query failed")
	}
	defer response.Body.Close()
	var result struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Value []json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 16384)).Decode(&result) != nil || result.Status != "success" || len(result.Data.Result) != 1 || len(result.Data.Result[0].Value) != 2 {
		return nil, errors.New("real Prometheus metric is missing")
	}
	var timestamp float64
	var value string
	if json.Unmarshal(result.Data.Result[0].Value[0], &timestamp) != nil || json.Unmarshal(result.Data.Result[0].Value[1], &value) != nil {
		return nil, errors.New("Prometheus sample is invalid")
	}
	count, err := strconv.ParseFloat(value, 64)
	if err != nil || count <= 0 || math.IsInf(count, 0) || math.IsNaN(count) || math.IsInf(timestamp, 0) || math.IsNaN(timestamp) ||
		time.Since(time.UnixMilli(int64(timestamp*1000))) > 15*time.Second || time.Until(time.UnixMilli(int64(timestamp*1000))) > 5*time.Second {
		return nil, errors.New("Prometheus failure sample is stale or empty")
	}
	logs, err := exec.CommandContext(ctx, "docker", "logs", "--tail", "12", "--since", "30s", "--", c.cfg.ContainerName).CombinedOutput()
	if err != nil || len(logs) == 0 {
		return nil, errors.New("fresh application logs are missing")
	}
	text := security.Redact(string(logs))
	if len(text) > 1400 {
		text = text[len(text)-1400:]
	}
	now := time.Now().UTC()
	metadata, _ := json.Marshal(map[string]any{"target": c.cfg.Binding.ServiceName, "complete": true, "deployed_revision": c.cfg.BaseSHA,
		"provenance": "trusted-host-docker-inspect", "image_id": container.Image})
	return []domain.EvidenceItem{
		{ID: uuid.NewString(), IncidentID: incidentID, Type: "metric", Source: "workload-control", Timestamp: now,
			Snippet: "Real Prometheus query: " + query + "; health endpoint HTTP 500 count=" + value + "; trusted Docker image matches the approved revision.", MetadataJSON: string(metadata)},
		{ID: uuid.NewString(), IncidentID: incidentID, Type: "log", Source: "docker-application-logs", Timestamp: now, Snippet: text, MetadataJSON: `{}`},
	}, nil
}
