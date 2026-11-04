package verification

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

type httpProfileClient struct {
	root     string
	contract *coderepair.TelemetryContract
	queries  HTTPQueries
	client   *http.Client
}

func newHTTPClient(root string, contract *coderepair.TelemetryContract) (*httpProfileClient, error) {
	root, ok := httpRoot(root)
	if !ok {
		return nil, errors.New("HTTP telemetry requires the configured Prometheus endpoint")
	}
	queries, err := RenderHTTPQueries(*contract)
	if err != nil {
		return nil, err
	}
	return &httpProfileClient{root: root, contract: contract, queries: queries, client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

type metricPoint struct {
	Metric map[string]string `json:"metric"`
	Value  []json.RawMessage `json:"value"`
}

func (c *httpProfileClient) query(ctx context.Context, query string) ([]metricPoint, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.root+"/api/v1/query?"+url.Values{"query": {query}}.Encode(), nil)
	if err != nil {
		return nil, err
	}
	response, err := c.client.Do(req)
	if err != nil {
		return nil, errors.New("Prometheus observation unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("Prometheus query returned non-success")
	}
	var payload struct {
		Status   string   `json:"status"`
		Warnings []string `json:"warnings"`
		Data     struct {
			ResultType string        `json:"resultType"`
			Result     []metricPoint `json:"result"`
		} `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 128*1024)).Decode(&payload) != nil || payload.Status != "success" || payload.Data.ResultType != "vector" || len(payload.Data.Result) == 0 || len(payload.Data.Result) > 200 || len(payload.Warnings) > 0 {
		return nil, errors.New("Prometheus observation is missing, ambiguous or incomplete")
	}
	return payload.Data.Result, nil
}

func pointValue(p metricPoint, now time.Time) (float64, time.Time, error) {
	if len(p.Value) != 2 {
		return 0, time.Time{}, errors.New("invalid metric point")
	}
	var at float64
	var raw string
	if json.Unmarshal(p.Value[0], &at) != nil || json.Unmarshal(p.Value[1], &raw) != nil || !finite(at) {
		return 0, time.Time{}, errors.New("invalid metric point")
	}
	v, err := strconv.ParseFloat(raw, 64)
	ts := time.Unix(0, int64(at*1e9)).UTC()
	if err != nil || !finite(v) || ts.After(now.Add(5*time.Second)) || now.Sub(ts) > time.Minute {
		return 0, time.Time{}, errors.New("metric point is stale or non-finite")
	}
	return v, ts, nil
}

func (c *httpProfileClient) Snapshot(ctx context.Context) (Sample, error) {
	now := time.Now().UTC()
	h := c.contract.Profile.HTTP
	s := Sample{Target: c.contract.ServiceName, Environment: c.contract.Environment, ProfileSHA256: c.contract.Digest(), Source: "prometheus-http-v1", Timestamp: now, Complete: true, HTTP: &HTTPObservation{Healthy: true}}
	instances := map[string]map[string]bool{}
	for _, kind := range []string{"requests", "health", "revision"} {
		query := c.queries.Requests
		if kind == "health" {
			query = c.queries.Health
		}
		if kind == "revision" {
			query = c.queries.Revision
		}
		points, err := c.query(ctx, query)
		if err != nil {
			return Sample{}, err
		}
		seen := map[string]bool{}
		if kind != "requests" {
			instances[kind] = seen
		}
		for _, point := range points {
			v, at, err := pointValue(point, now)
			if err != nil {
				return Sample{}, err
			}
			if point.Metric[h.ServiceLabel] != s.Target || point.Metric[h.EnvironmentLabel] != s.Environment {
				return Sample{}, errors.New("metric target does not match registered binding")
			}
			if at.Before(s.Timestamp) {
				s.Timestamp = at
			}
			switch kind {
			case "requests":
				raw := point.Metric[h.StatusLabel]
				status, err := strconv.Atoi(raw)
				if err != nil || status < 100 || status > 599 || len(raw) != 3 || seen[raw] || v < 0 {
					return Sample{}, errors.New("HTTP status counters are invalid or duplicated")
				}
				seen[raw] = true
				s.HTTP.Requests += v
				if status >= 500 {
					s.HTTP.Errors += v
				}
			case "health":
				instance := point.Metric["instance"]
				if seen[instance] || (instance == "" && len(points) > 1) {
					return Sample{}, errors.New("health instances are missing or duplicated")
				}
				seen[instance] = true
				if v != 0 && v != 1 {
					return Sample{}, errors.New("health metric must be zero or one")
				}
				s.HTTP.Healthy = s.HTTP.Healthy && v == 1
			case "revision":
				instance := point.Metric["instance"]
				if seen[instance] || (instance == "" && len(points) > 1) {
					return Sample{}, errors.New("revision instances are missing or duplicated")
				}
				seen[instance] = true
				revision := point.Metric[h.RevisionLabel]
				if v != 1 || !coderepair.ValidGitRevision(revision) || (s.DeployedRevision != "" && s.DeployedRevision != revision) {
					return Sample{}, errors.New("deployed revision is missing or mixed across instances")
				}
				s.DeployedRevision = revision
			}
		}
	}
	if len(instances["health"]) != len(instances["revision"]) {
		return Sample{}, errors.New("health and revision observations cover different instances")
	}
	for instance := range instances["health"] {
		if !instances["revision"][instance] {
			return Sample{}, errors.New("health instance has no matching deployed revision")
		}
	}
	cleared, err := readAlertStatus(ctx, c.client, c.root+"/api/v1/alerts", c.root+"/api/v1/rules?type=alert", c.contract)
	if err != nil {
		return Sample{}, err
	}
	s.AlertObserved, s.AlertCleared = true, cleared
	if !s.Valid(time.Now().UTC()) || !s.Matches(c.contract) {
		return Sample{}, errors.New("HTTP observation does not match registered profile")
	}
	return s, nil
}
