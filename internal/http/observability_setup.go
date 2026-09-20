package http

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/observability"
)

type observabilitySetupForm struct {
	BaseURL           string
	APIToken          string
	MetricsSourceUID  string
	LogsSourceUID     string
	ErrorRateQuery    string
	LatencyQuery      string
	QueueQuery        string
	ReplicaQuery      string
	LogsQuery         string
	DeployLogsQuery   string
	SampleService     string
	SampleEnvironment string
	SampleSeverity    string
	SampleTitle       string
}

type observabilityConnectionResult struct {
	Attempted bool
	Summary   string
	Error     string
}

type observabilityQueryResult struct {
	Name    string
	Source  string
	Query   string
	Status  string
	Preview string
	Error   string
}

type observabilityReadinessItem struct {
	Label  string
	Ready  bool
	Detail string
}

type observabilityProfileState struct {
	Path   string
	Loaded bool
	Error  string
}

type observabilitySetupPageData struct {
	Form          observabilitySetupForm
	Runtime       runtimeViewData
	LocalModeNote string
	Profile       observabilityProfileState
	Connection    observabilityConnectionResult
	Datasources   []observability.DatasourceSummary
	QueryResults  []observabilityQueryResult
	Evidence      []domain.EvidenceItem
	Readiness     []observabilityReadinessItem
	Notice        string
	Error         string
}

func defaultObservabilitySetupForm(cfg config.Config) observabilitySetupForm {
	return observabilitySetupForm{
		BaseURL:           cfg.GrafanaBaseURL,
		APIToken:          cfg.GrafanaAPIToken,
		MetricsSourceUID:  cfg.GrafanaMetricsSourceUID,
		LogsSourceUID:     cfg.GrafanaLogsSourceUID,
		ErrorRateQuery:    cfg.GrafanaErrorRateQuery,
		LatencyQuery:      cfg.GrafanaLatencyQuery,
		QueueQuery:        cfg.GrafanaQueueQuery,
		ReplicaQuery:      cfg.GrafanaReplicaQuery,
		LogsQuery:         cfg.GrafanaLogsQuery,
		DeployLogsQuery:   cfg.GrafanaDeployLogsQuery,
		SampleService:     "checkout-service",
		SampleEnvironment: "staging",
		SampleSeverity:    "critical",
		SampleTitle:       "checkout timeout after deploy",
	}
}

func defaultObservabilityProfileState(cfg config.Config) observabilityProfileState {
	return observabilityProfileState{
		Path:   cfg.LocalObservabilityProfilePath,
		Loaded: cfg.LocalObservabilityProfileLoaded,
		Error:  cfg.LocalObservabilityProfileError,
	}
}

func loadObservabilitySetupDefaults(cfg config.Config) (observabilitySetupForm, observabilityProfileState) {
	form := defaultObservabilitySetupForm(cfg)
	state := defaultObservabilityProfileState(cfg)

	profile, loaded, err := config.LoadObservabilityProfile(state.Path)
	if err != nil {
		state.Loaded = false
		state.Error = err.Error()
		return form, state
	}

	state.Loaded = loaded
	state.Error = ""
	if !loaded {
		return form, state
	}

	return applyObservabilityProfile(form, profile), state
}

func applyObservabilityProfile(form observabilitySetupForm, profile config.ObservabilityProfile) observabilitySetupForm {
	if profile.BaseURL != "" {
		form.BaseURL = profile.BaseURL
	}
	if profile.APIToken != "" {
		form.APIToken = profile.APIToken
	}
	if profile.MetricsSourceUID != "" {
		form.MetricsSourceUID = profile.MetricsSourceUID
	}
	if profile.LogsSourceUID != "" {
		form.LogsSourceUID = profile.LogsSourceUID
	}
	if profile.ErrorRateQuery != "" {
		form.ErrorRateQuery = profile.ErrorRateQuery
	}
	if profile.LatencyQuery != "" {
		form.LatencyQuery = profile.LatencyQuery
	}
	if profile.QueueQuery != "" {
		form.QueueQuery = profile.QueueQuery
	}
	if profile.ReplicaQuery != "" {
		form.ReplicaQuery = profile.ReplicaQuery
	}
	if profile.LogsQuery != "" {
		form.LogsQuery = profile.LogsQuery
	}
	if profile.DeployLogsQuery != "" {
		form.DeployLogsQuery = profile.DeployLogsQuery
	}

	return form
}

func profileFromObservabilitySetupForm(form observabilitySetupForm) config.ObservabilityProfile {
	return config.ObservabilityProfile{
		BaseURL:          form.BaseURL,
		APIToken:         form.APIToken,
		MetricsSourceUID: form.MetricsSourceUID,
		LogsSourceUID:    form.LogsSourceUID,
		ErrorRateQuery:   form.ErrorRateQuery,
		LatencyQuery:     form.LatencyQuery,
		QueueQuery:       form.QueueQuery,
		ReplicaQuery:     form.ReplicaQuery,
		LogsQuery:        form.LogsQuery,
		DeployLogsQuery:  form.DeployLogsQuery,
	}
}

func parseObservabilitySetupForm(r *http.Request, fallback observabilitySetupForm) (observabilitySetupForm, error) {
	if err := r.ParseForm(); err != nil {
		return observabilitySetupForm{}, fmt.Errorf("invalid setup form payload")
	}

	valueOr := func(key string, fallbackValue string) string {
		value := strings.TrimSpace(r.FormValue(key))
		if value == "" {
			return fallbackValue
		}
		return value
	}

	return observabilitySetupForm{
		BaseURL:           valueOr("grafana_base_url", fallback.BaseURL),
		APIToken:          valueOr("grafana_api_token", fallback.APIToken),
		MetricsSourceUID:  valueOr("grafana_metrics_datasource_uid", fallback.MetricsSourceUID),
		LogsSourceUID:     valueOr("grafana_logs_datasource_uid", fallback.LogsSourceUID),
		ErrorRateQuery:    valueOr("grafana_error_rate_query", fallback.ErrorRateQuery),
		LatencyQuery:      valueOr("grafana_latency_query", fallback.LatencyQuery),
		QueueQuery:        valueOr("grafana_queue_query", fallback.QueueQuery),
		ReplicaQuery:      valueOr("grafana_replica_query", fallback.ReplicaQuery),
		LogsQuery:         valueOr("grafana_logs_query", fallback.LogsQuery),
		DeployLogsQuery:   valueOr("grafana_deploy_logs_query", fallback.DeployLogsQuery),
		SampleService:     valueOr("sample_service", fallback.SampleService),
		SampleEnvironment: valueOr("sample_environment", fallback.SampleEnvironment),
		SampleSeverity:    valueOr("sample_severity", fallback.SampleSeverity),
		SampleTitle:       valueOr("sample_title", fallback.SampleTitle),
	}, nil
}

func newSetupGrafanaClient(form observabilitySetupForm) *observability.GrafanaClient {
	return observability.NewGrafanaClient(
		form.BaseURL,
		form.APIToken,
		form.MetricsSourceUID,
		form.LogsSourceUID,
	)
}

func newSetupSignalConfig(form observabilitySetupForm) observability.GrafanaSignalConfig {
	return observability.GrafanaSignalConfig{
		ErrorRateQuery:  form.ErrorRateQuery,
		LatencyQuery:    form.LatencyQuery,
		QueueQuery:      form.QueueQuery,
		ReplicaQuery:    form.ReplicaQuery,
		LogsQuery:       form.LogsQuery,
		DeployLogsQuery: form.DeployLogsQuery,
		Lookback:        15 * time.Minute,
	}
}

func buildSetupIncident(form observabilitySetupForm) domain.Incident {
	return domain.Incident{
		ID:          "observability-setup-preview",
		Title:       form.SampleTitle,
		ServiceName: form.SampleService,
		Environment: form.SampleEnvironment,
		Severity:    form.SampleSeverity,
	}
}

func buildLocalModeNote() string {
	return "This page is designed for a 100% local, single-user workflow. Query tests run in memory, and any saved observability profile stays on this machine only. Saved values become the next startup default unless environment variables override them."
}

func runObservabilityConnectionTest(ctx context.Context, form observabilitySetupForm) (observabilityConnectionResult, []observability.DatasourceSummary) {
	client := newSetupGrafanaClient(form)
	if !client.Configured() {
		return observabilityConnectionResult{
			Attempted: true,
			Error:     "grafana_base_url and grafana_api_token are required",
		}, nil
	}

	items, err := client.ListDatasources(ctx)
	if err != nil {
		return observabilityConnectionResult{
			Attempted: true,
			Error:     err.Error(),
		}, nil
	}

	return observabilityConnectionResult{
		Attempted: true,
		Summary:   fmt.Sprintf("Connected successfully. %d datasource(s) discovered.", len(items)),
	}, items
}

func runObservabilityQueryPreview(ctx context.Context, form observabilitySetupForm) (observabilityConnectionResult, []observability.DatasourceSummary, []observabilityQueryResult, []domain.EvidenceItem) {
	connection, datasources := runObservabilityConnectionTest(ctx, form)
	if connection.Error != "" {
		return connection, datasources, nil, nil
	}

	client := newSetupGrafanaClient(form)
	incidentRecord := buildSetupIncident(form)
	results := make([]observabilityQueryResult, 0, 6)
	now := time.Now().UTC()

	for _, item := range []struct {
		Name    string
		Source  string
		Query   string
		IsLogs  bool
		Preview func(float64) string
	}{
		{Name: "Error Rate", Source: "prometheus", Query: renderSetupQueryTemplate(form.ErrorRateQuery, incidentRecord), Preview: func(v float64) string { return fmt.Sprintf("%.4f", v) }},
		{Name: "Latency", Source: "prometheus", Query: renderSetupQueryTemplate(form.LatencyQuery, incidentRecord), Preview: func(v float64) string { return fmt.Sprintf("%.2f ms", v) }},
		{Name: "Queue Backlog", Source: "prometheus", Query: renderSetupQueryTemplate(form.QueueQuery, incidentRecord), Preview: func(v float64) string { return fmt.Sprintf("%.0f items", v) }},
		{Name: "Replica Count", Source: "prometheus", Query: renderSetupQueryTemplate(form.ReplicaQuery, incidentRecord), Preview: func(v float64) string { return fmt.Sprintf("%.0f replicas", v) }},
		{Name: "Incident Logs", Source: "loki", Query: renderSetupQueryTemplate(form.LogsQuery, incidentRecord), IsLogs: true},
		{Name: "Deploy Logs", Source: "loki", Query: renderSetupQueryTemplate(form.DeployLogsQuery, incidentRecord), IsLogs: true},
	} {
		if strings.TrimSpace(item.Query) == "" {
			continue
		}

		if item.IsLogs {
			lines, err := client.LokiLines(ctx, item.Query, now.Add(-15*time.Minute), now, 3)
			if err != nil {
				results = append(results, observabilityQueryResult{
					Name:   item.Name,
					Source: item.Source,
					Query:  item.Query,
					Status: "error",
					Error:  err.Error(),
				})
				continue
			}
			results = append(results, observabilityQueryResult{
				Name:    item.Name,
				Source:  item.Source,
				Query:   item.Query,
				Status:  "ok",
				Preview: strings.Join(lines, " | "),
			})
			continue
		}

		value, err := client.PrometheusInstantValue(ctx, item.Query, now)
		if err != nil {
			results = append(results, observabilityQueryResult{
				Name:   item.Name,
				Source: item.Source,
				Query:  item.Query,
				Status: "error",
				Error:  err.Error(),
			})
			continue
		}
		results = append(results, observabilityQueryResult{
			Name:    item.Name,
			Source:  item.Source,
			Query:   item.Query,
			Status:  "ok",
			Preview: item.Preview(value),
		})
	}

	evidence, _ := observability.NewGrafanaCollector(client, newSetupSignalConfig(form)).Collect(ctx, incidentRecord)
	return connection, datasources, results, evidence
}

func renderSetupQueryTemplate(query string, incidentRecord domain.Incident) string {
	replacer := strings.NewReplacer(
		"{{service}}", incidentRecord.ServiceName,
		"{{environment}}", incidentRecord.Environment,
		"{{severity}}", incidentRecord.Severity,
		"{{title}}", incidentRecord.Title,
	)
	return strings.TrimSpace(replacer.Replace(query))
}

func buildObservabilityReadiness(form observabilitySetupForm, connection observabilityConnectionResult, results []observabilityQueryResult) []observabilityReadinessItem {
	items := []observabilityReadinessItem{
		{
			Label:  "Grafana connection",
			Ready:  connection.Attempted && connection.Error == "",
			Detail: chooseReadinessDetail(connection.Attempted && connection.Error == "", "Connection test succeeded.", "Grafana connection still needs a successful test."),
		},
		{
			Label:  "Datasource selection",
			Ready:  strings.TrimSpace(form.MetricsSourceUID) != "" && strings.TrimSpace(form.LogsSourceUID) != "",
			Detail: chooseReadinessDetail(strings.TrimSpace(form.MetricsSourceUID) != "" && strings.TrimSpace(form.LogsSourceUID) != "", "Metrics and logs datasource UIDs are filled.", "Metrics and logs datasource UIDs should both be set."),
		},
		{
			Label:  "Metric queries",
			Ready:  strings.TrimSpace(form.ErrorRateQuery) != "" && strings.TrimSpace(form.LatencyQuery) != "",
			Detail: chooseReadinessDetail(strings.TrimSpace(form.ErrorRateQuery) != "" && strings.TrimSpace(form.LatencyQuery) != "", "Core metric queries are present.", "At least error-rate and latency queries should be defined."),
		},
		{
			Label:  "Evidence preview",
			Ready:  countSuccessfulQueryResults(results) >= 2,
			Detail: chooseReadinessDetail(countSuccessfulQueryResults(results) >= 2, "Query preview returned usable signals.", "Run query preview until at least a couple of signals return successfully."),
		},
	}
	return items
}

func chooseReadinessDetail(ready bool, success string, failure string) string {
	if ready {
		return success
	}
	return failure
}

func countSuccessfulQueryResults(results []observabilityQueryResult) int {
	count := 0
	for _, result := range results {
		if result.Status == "ok" {
			count++
		}
	}
	return count
}

var observabilitySetupTemplate = template.Must(template.New("observability-setup").Funcs(templateFuncs).Parse(`
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>Triovexa Observability Setup</title>
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="stylesheet" href="/ui/assets/workbench.css">
</head>
<body class="page-detail">
  <div class="shell">
    <p><a href="/ui/incidents">Back to incident workbench</a></p>
    <section class="hero">
      <article class="hero-card">
        <p class="eyebrow">Observability Setup</p>
        <h1>Grafana Setup Preview</h1>
        <p class="muted">Lightweight setup assistant for a local single-user workflow. Test Grafana connection, inspect datasources, try query templates, and preview normalized evidence without adding frontend runtime or shared infrastructure.</p>
        <p><span class="chip">Observability {{.Runtime.ObservabilityMode}}</span> <span class="chip">Reasoning {{.Runtime.ReasoningMode}}</span></p>
      </article>
      <article class="hero-card">
        <p class="eyebrow">Local-First Note</p>
        <h2 class="tight">Ephemeral by Design</h2>
        <p class="muted">{{.LocalModeNote}}</p>
        <p class="muted"><strong>Local profile path:</strong> {{if .Profile.Path}}<code>{{.Profile.Path}}</code>{{else}}not configured{{end}}</p>
        {{if .Profile.Loaded}}<p class="chip">Saved local profile found</p>{{else}}<p class="chip">No saved local profile yet</p>{{end}}
        {{if .Profile.Error}}<p class="banner error">{{.Profile.Error}}</p>{{end}}
        {{if .Connection.Summary}}<p class="chip">{{.Connection.Summary}}</p>{{end}}
        {{if .Connection.Error}}<p class="chip">{{.Connection.Error}}</p>{{end}}
      </article>
    </section>
    {{if .Notice}}
    <p class="banner success">{{.Notice}}</p>
    {{end}}
    {{if .Error}}
    <p class="banner error">{{.Error}}</p>
    {{end}}

    <section class="panel">
      <h2>Connection, Datasources, And Query Templates</h2>
      <p class="muted">Use this single form to test connection, save a local profile, or run a full evidence preview. Save only writes to a machine-local profile file and does not touch the database.</p>
      <form method="post" action="/ui/setup/observability/test-query" class="setup-form">
        <div class="setup-grid">
          <label>
            <span>Grafana Base URL</span>
            <input type="text" name="grafana_base_url" value="{{.Form.BaseURL}}" placeholder="https://example.grafana.net">
          </label>
          <label>
            <span>Grafana API Token</span>
            <input type="password" name="grafana_api_token" value="{{.Form.APIToken}}" placeholder="glsa_...">
          </label>
          <label>
            <span>Metrics Datasource UID</span>
            <input type="text" name="grafana_metrics_datasource_uid" value="{{.Form.MetricsSourceUID}}" placeholder="grafanacloud-prom">
          </label>
          <label>
            <span>Logs Datasource UID</span>
            <input type="text" name="grafana_logs_datasource_uid" value="{{.Form.LogsSourceUID}}" placeholder="grafanacloud-logs">
          </label>
        </div>

        <div class="setup-grid">
          <label>
            <span>Error Rate Query</span>
            <textarea name="grafana_error_rate_query" rows="3" placeholder="rate(http_requests_total{service=&quot;sample-service&quot;}[5m])">{{.Form.ErrorRateQuery}}</textarea>
          </label>
          <label>
            <span>Latency Query</span>
            <textarea name="grafana_latency_query" rows="3" placeholder="histogram_quantile(0.95, ...)">{{.Form.LatencyQuery}}</textarea>
          </label>
          <label>
            <span>Queue Query</span>
            <textarea name="grafana_queue_query" rows="3" placeholder="queue_backlog{service=&quot;sample-service&quot;}">{{.Form.QueueQuery}}</textarea>
          </label>
          <label>
            <span>Replica Query</span>
            <textarea name="grafana_replica_query" rows="3" placeholder="kube_deployment_status_replicas{...}">{{.Form.ReplicaQuery}}</textarea>
          </label>
          <label>
            <span>Incident Logs Query</span>
            <textarea name="grafana_logs_query" rows="3" placeholder="{service=&quot;sample-service&quot;,env=&quot;staging&quot;}">{{.Form.LogsQuery}}</textarea>
          </label>
          <label>
            <span>Deploy Logs Query</span>
            <textarea name="grafana_deploy_logs_query" rows="3" placeholder="{service=&quot;sample-service&quot;} |= &quot;deploy&quot;">{{.Form.DeployLogsQuery}}</textarea>
          </label>
        </div>

        <div class="setup-grid compact">
          <label>
            <span>Sample Service</span>
            <input type="text" name="sample_service" value="{{.Form.SampleService}}">
          </label>
          <label>
            <span>Sample Environment</span>
            <input type="text" name="sample_environment" value="{{.Form.SampleEnvironment}}">
          </label>
          <label>
            <span>Sample Severity</span>
            <input type="text" name="sample_severity" value="{{.Form.SampleSeverity}}">
          </label>
          <label>
            <span>Sample Title</span>
            <input type="text" name="sample_title" value="{{.Form.SampleTitle}}">
          </label>
        </div>

        <div class="hero-links">
          <button class="secondary-button" type="submit" formaction="/ui/setup/observability/test-connection">Test Connection</button>
          <button class="secondary-button" type="submit" formaction="/ui/setup/observability/save-profile">Save Local Profile</button>
          <button class="primary-button" type="submit">Run Query Preview</button>
        </div>
      </form>
      <form method="post" action="/ui/setup/observability/clear-profile" class="control-form">
        <button class="secondary-button" type="submit">Clear Saved Profile</button>
      </form>
      <p class="muted">Saving a profile helps future local runs start with the same Grafana defaults. Runtime provider wiring in the current server process still follows the values loaded at startup.</p>
    </section>

    <section class="workbench-grid">
      <article class="panel">
        <h2>Readiness Summary</h2>
        <div class="scenario-grid">
          {{range .Readiness}}
          <article class="scenario-card">
            <div class="hero-links">
              <strong>{{.Label}}</strong>
              <span class="chip">{{if .Ready}}ready{{else}}needs work{{end}}</span>
            </div>
            <p class="muted">{{.Detail}}</p>
          </article>
          {{end}}
        </div>
      </article>

      <article class="panel">
        <h2>Current Runtime Context</h2>
        <p class="muted">The active modes are not changed automatically from this page.</p>
        <p><span class="chip">Reasoning {{.Runtime.ReasoningMode}}</span> <span class="chip">Observability {{.Runtime.ObservabilityMode}}</span></p>
        <p class="muted">Configured metrics UID: {{if .Runtime.MetricsSourceUID}}{{.Runtime.MetricsSourceUID}}{{else}}-{{end}}</p>
        <p class="muted">Configured logs UID: {{if .Runtime.LogsSourceUID}}{{.Runtime.LogsSourceUID}}{{else}}-{{end}}</p>
      </article>
    </section>

    <section class="workbench-grid">
      <article class="panel">
        <h2>Datasource Discovery</h2>
        {{if .Datasources}}
        <div class="table-wrap">
          <table>
            <thead>
              <tr><th>Name</th><th>Type</th><th>UID</th><th>Flags</th></tr>
            </thead>
            <tbody>
            {{range .Datasources}}
              <tr>
                <td>{{.Name}}</td>
                <td>{{.Type}}</td>
                <td><code>{{.UID}}</code></td>
                <td>{{if .Default}}default {{end}}{{if .ReadOnly}}read-only{{end}}</td>
              </tr>
            {{end}}
            </tbody>
          </table>
        </div>
        {{else}}
          <p class="muted">Run a connection test to load datasources.</p>
        {{end}}
      </article>

      <article class="panel">
        <h2>Query Test Results</h2>
        {{if .QueryResults}}
          <div class="scenario-grid">
          {{range .QueryResults}}
            <article class="scenario-card">
              <div class="hero-links">
                <strong>{{.Name}}</strong>
                <span class="chip">{{.Source}}</span>
                <span class="chip">{{.Status}}</span>
              </div>
              <p><code>{{.Query}}</code></p>
              {{if .Preview}}<p class="muted">{{.Preview}}</p>{{end}}
              {{if .Error}}<p class="banner error">{{.Error}}</p>{{end}}
            </article>
          {{end}}
          </div>
        {{else}}
          <p class="muted">Run query preview to see per-query results.</p>
        {{end}}
      </article>
    </section>

    <section class="panel">
      <h2>Evidence Preview</h2>
      {{if .Evidence}}
        <div class="table-wrap">
          <table>
            <thead>
              <tr><th>Type</th><th>Source</th><th>Snippet</th><th>Observed</th></tr>
            </thead>
            <tbody>
            {{range .Evidence}}
              <tr>
                <td>{{.Type}}</td>
                <td>{{.Source}}</td>
                <td>{{.Snippet}}</td>
                <td>{{formatTime .Timestamp}}</td>
              </tr>
            {{end}}
            </tbody>
          </table>
        </div>
      {{else}}
        <p class="muted">No evidence preview yet. Use a query preview run to inspect what would enter the reasoning layer.</p>
      {{end}}
    </section>
  </div>
</body>
</html>
`))

func renderObservabilitySetupPage(w http.ResponseWriter, data observabilitySetupPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = observabilitySetupTemplate.Execute(w, data)
}
