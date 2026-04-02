package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type DatasourceSummary struct {
	UID      string `json:"uid"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Default  bool   `json:"default"`
	ReadOnly bool   `json:"read_only"`
}

type GrafanaClient struct {
	baseURL          string
	apiToken         string
	metricsSourceUID string
	logsSourceUID    string
	client           *http.Client
}

func NewGrafanaClient(baseURL, apiToken, metricsSourceUID, logsSourceUID string) *GrafanaClient {
	return &GrafanaClient{
		baseURL:          strings.TrimRight(baseURL, "/"),
		apiToken:         strings.TrimSpace(apiToken),
		metricsSourceUID: strings.TrimSpace(metricsSourceUID),
		logsSourceUID:    strings.TrimSpace(logsSourceUID),
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *GrafanaClient) Configured() bool {
	return c.baseURL != "" && c.apiToken != ""
}

func (c *GrafanaClient) ListDatasources(ctx context.Context) ([]DatasourceSummary, error) {
	if !c.Configured() {
		return nil, fmt.Errorf("grafana client is not configured")
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/datasources", nil)
	if err != nil {
		return nil, fmt.Errorf("create datasource list request: %w", err)
	}
	c.authorize(request)

	response, err := c.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("list grafana datasources: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("grafana datasource list returned %d", response.StatusCode)
	}

	var raw []struct {
		UID      string `json:"uid"`
		Name     string `json:"name"`
		Type     string `json:"type"`
		Default  bool   `json:"isDefault"`
		ReadOnly bool   `json:"readOnly"`
	}
	if err := json.NewDecoder(response.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode grafana datasources: %w", err)
	}

	items := make([]DatasourceSummary, 0, len(raw))
	for _, item := range raw {
		items = append(items, DatasourceSummary{
			UID:      item.UID,
			Name:     item.Name,
			Type:     item.Type,
			Default:  item.Default,
			ReadOnly: item.ReadOnly,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Name < items[j].Name
	})

	return items, nil
}

func (c *GrafanaClient) PrometheusInstantValue(ctx context.Context, expr string, observedAt time.Time) (float64, error) {
	if !c.Configured() {
		return 0, fmt.Errorf("grafana client is not configured")
	}
	if strings.TrimSpace(c.metricsSourceUID) == "" {
		return 0, fmt.Errorf("grafana metrics datasource uid is not configured")
	}
	if strings.TrimSpace(expr) == "" {
		return 0, fmt.Errorf("prometheus query is empty")
	}

	endpoint := fmt.Sprintf("%s/api/datasources/proxy/uid/%s/api/v1/query", c.baseURL, c.metricsSourceUID)
	query := url.Values{}
	query.Set("query", expr)
	query.Set("time", strconv.FormatInt(observedAt.UTC().Unix(), 10))

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return 0, fmt.Errorf("create prometheus proxy request: %w", err)
	}
	c.authorize(request)

	response, err := c.client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("query prometheus datasource: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode >= http.StatusBadRequest {
		return 0, fmt.Errorf("prometheus datasource returned %d", response.StatusCode)
	}

	var payload struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Value []any `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return 0, fmt.Errorf("decode prometheus response: %w", err)
	}
	if payload.Status != "success" {
		return 0, fmt.Errorf("prometheus query did not succeed")
	}
	if len(payload.Data.Result) == 0 || len(payload.Data.Result[0].Value) < 2 {
		return 0, fmt.Errorf("prometheus query returned no datapoints")
	}

	rawValue, ok := payload.Data.Result[0].Value[1].(string)
	if !ok {
		return 0, fmt.Errorf("prometheus response value is not a string")
	}

	value, err := strconv.ParseFloat(rawValue, 64)
	if err != nil {
		return 0, fmt.Errorf("parse prometheus value: %w", err)
	}

	return value, nil
}

func (c *GrafanaClient) LokiLines(ctx context.Context, queryExpr string, start, end time.Time, limit int) ([]string, error) {
	if !c.Configured() {
		return nil, fmt.Errorf("grafana client is not configured")
	}
	if strings.TrimSpace(c.logsSourceUID) == "" {
		return nil, fmt.Errorf("grafana logs datasource uid is not configured")
	}
	if strings.TrimSpace(queryExpr) == "" {
		return nil, fmt.Errorf("loki query is empty")
	}
	if limit <= 0 {
		limit = 20
	}

	endpoint := fmt.Sprintf("%s/api/datasources/proxy/uid/%s/loki/api/v1/query_range", c.baseURL, c.logsSourceUID)
	query := url.Values{}
	query.Set("query", queryExpr)
	query.Set("start", strconv.FormatInt(start.UTC().UnixNano(), 10))
	query.Set("end", strconv.FormatInt(end.UTC().UnixNano(), 10))
	query.Set("limit", strconv.Itoa(limit))
	query.Set("direction", "backward")

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("create loki proxy request: %w", err)
	}
	c.authorize(request)

	response, err := c.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("query loki datasource: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("loki datasource returned %d", response.StatusCode)
	}

	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Values [][]string `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode loki response: %w", err)
	}
	if payload.Status != "success" {
		return nil, fmt.Errorf("loki query did not succeed")
	}

	lines := make([]string, 0, limit)
	for _, stream := range payload.Data.Result {
		for _, entry := range stream.Values {
			if len(entry) < 2 {
				continue
			}
			line := strings.TrimSpace(entry[1])
			if line == "" {
				continue
			}
			lines = append(lines, line)
			if len(lines) >= limit {
				return lines, nil
			}
		}
	}

	if len(lines) == 0 {
		return nil, fmt.Errorf("loki query returned no log lines")
	}

	return lines, nil
}

func (c *GrafanaClient) authorize(request *http.Request) {
	request.Header.Set("Authorization", "Bearer "+c.apiToken)
	request.Header.Set("Accept", "application/json")
}
