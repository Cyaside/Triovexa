package verification

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

type Sample struct {
	Target           string    `json:"target"`
	Source           string    `json:"source"`
	Timestamp        time.Time `json:"timestamp"`
	Complete         bool      `json:"complete"`
	WorkerHealthy    bool      `json:"worker_healthy"`
	ConsumerPaused   bool      `json:"consumer_paused"`
	QueueBacklog     int64     `json:"queue_backlog"`
	JobsProcessed    int64     `json:"jobs_processed"`
	Errors           int64     `json:"errors"`
	DeployedRevision string    `json:"deployed_revision"`
	AlertObserved    bool      `json:"alert_observed"`
	AlertCleared     bool      `json:"alert_cleared"`
}

// Deployment ties verification to the revision observed after a reviewed PR.
type Deployment struct {
	ID                 string
	CaseID             string
	Environment        string
	RevisionSHA        string
	DeploymentID       string
	ObservedAt         time.Time
	Phase              string
	VerificationStatus string
	Baseline           Sample
	CompletedAt        time.Time
	LeaseToken         string
	LeaseUntil         time.Time
	Attempts           int
}

func (s Sample) Valid(now time.Time) bool {
	return s.Complete && s.AlertObserved && s.Target == "queue-worker" && s.Source == "redis-streams" &&
		coderepair.ValidGitRevision(s.DeployedRevision) && !s.Timestamp.IsZero() &&
		!s.Timestamp.After(now.Add(5*time.Second)) && now.Sub(s.Timestamp) <= time.Minute &&
		s.QueueBacklog >= 0 && s.JobsProcessed >= 0 && s.Errors >= 0
}

type WorkloadClient struct {
	endpoint       string
	alertsEndpoint string
	rulesEndpoint  string
	token          string
	client         *http.Client
}

func NewWorkloadClient(baseURL, token, prometheusURL string) (*WorkloadClient, error) {
	baseURL, validControl := httpRoot(baseURL)
	prometheusURL, validPrometheus := httpRoot(prometheusURL)
	if !validControl || token == "" {
		return nil, errors.New("repair verification requires workload control endpoint and token")
	}
	if !validPrometheus {
		return nil, errors.New("repair verification requires Prometheus alert status")
	}
	return &WorkloadClient{endpoint: baseURL + "/state", alertsEndpoint: prometheusURL + "/api/v1/alerts",
		rulesEndpoint: prometheusURL + "/api/v1/rules?type=alert", token: token,
		client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func httpRoot(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Contains(u.Host, "@") {
		return "", false
	}
	return strings.TrimRight(u.String(), "/"), true
}

func (c *WorkloadClient) Snapshot(ctx context.Context) (Sample, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return Sample{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.client.Do(req)
	if err != nil {
		return Sample{}, errors.New("workload telemetry unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Sample{}, errors.New("workload telemetry returned non-success status")
	}
	var sample Sample
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&sample); err != nil {
		return Sample{}, errors.New("workload telemetry response is invalid")
	}
	cleared, err := c.alertCleared(ctx)
	if err != nil {
		return Sample{}, err
	}
	sample.AlertObserved = true
	sample.AlertCleared = cleared
	if !sample.Valid(time.Now().UTC()) {
		return Sample{}, errors.New("workload telemetry is incomplete or stale")
	}
	return sample, nil
}

func (c *WorkloadClient) alertCleared(ctx context.Context) (bool, error) {
	if err := c.requireAlertRule(ctx); err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.alertsEndpoint, nil)
	if err != nil {
		return false, err
	}
	response, err := c.client.Do(req)
	if err != nil {
		return false, errors.New("Prometheus alert status unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, errors.New("Prometheus alert status returned non-success")
	}
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Alerts []struct {
				Labels struct {
					AlertName string `json:"alertname"`
				} `json:"labels"`
				State string `json:"state"`
			} `json:"alerts"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&payload); err != nil || payload.Status != "success" {
		return false, errors.New("Prometheus alert status is invalid")
	}
	for _, alert := range payload.Data.Alerts {
		if alert.Labels.AlertName == "TriovexaQueueBacklogHigh" && alert.State != "inactive" {
			return false, nil
		}
	}
	return true, nil
}

func (c *WorkloadClient) requireAlertRule(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.rulesEndpoint, nil)
	if err != nil {
		return err
	}
	response, err := c.client.Do(req)
	if err != nil {
		return errors.New("Prometheus alert rule unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("Prometheus alert rule returned non-success")
	}
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Groups []struct {
				Rules []struct {
					Name   string `json:"name"`
					Type   string `json:"type"`
					Health string `json:"health"`
				} `json:"rules"`
			} `json:"groups"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 256<<10)).Decode(&payload); err != nil || payload.Status != "success" {
		return errors.New("Prometheus alert rule status is invalid")
	}
	for _, group := range payload.Data.Groups {
		for _, rule := range group.Rules {
			if rule.Name == "TriovexaQueueBacklogHigh" && rule.Type == "alerting" && rule.Health == "ok" {
				return nil
			}
		}
	}
	return errors.New("required Prometheus alert rule is missing or unhealthy")
}

// Check requires a new deployed revision and measurable worker progress.
// When the pre-deploy backlog was already empty or the worker was healthy,
// attribution to a code patch cannot be proven from this fixture.
func Check(before, after Sample, targetSHA string) bool {
	return before.Complete && before.QueueBacklog > 0 && !before.WorkerHealthy &&
		after.Complete && after.DeployedRevision == targetSHA &&
		after.Timestamp.After(before.Timestamp) && after.WorkerHealthy && !after.ConsumerPaused &&
		after.JobsProcessed > before.JobsProcessed &&
		(after.QueueBacklog < before.QueueBacklog || after.QueueBacklog == 0) &&
		after.Errors <= before.Errors && after.AlertObserved && after.AlertCleared
}
