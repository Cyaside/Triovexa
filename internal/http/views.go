package http

import (
	"bytes"
	"encoding/json"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/Cyaside/Triovexa/internal/domain"
)

type incidentListPageData struct {
	Incidents []domain.Incident
}

type incidentDetailPageData struct {
	Incident   domain.Incident
	Triage     *domain.TriageResult
	Evidence   []domain.EvidenceItem
	Documents  []domain.DocumentReference
	Actions    []domain.CandidateAction
	AuditTrail []domain.AuditEvent
}

var templateFuncs = template.FuncMap{
	"formatTime": func(value time.Time) string {
		if value.IsZero() {
			return "-"
		}
		return value.Format(time.RFC3339)
	},
	"formatJSON": func(raw string) string {
		if strings.TrimSpace(raw) == "" {
			return "{}"
		}

		var formatted bytes.Buffer
		if err := json.Indent(&formatted, []byte(raw), "", "  "); err != nil {
			return raw
		}

		return formatted.String()
	},
	"joinStrings": func(values []string) string {
		if len(values) == 0 {
			return "-"
		}
		return strings.Join(values, ", ")
	},
}

var incidentListTemplate = template.Must(template.New("incident-list").Funcs(templateFuncs).Parse(`
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>Triovexa Incident List</title>
  <style>
    body { font-family: Segoe UI, sans-serif; margin: 2rem; background: #f7f9fc; color: #132238; }
    h1 { margin-bottom: 0.5rem; }
    table { width: 100%; border-collapse: collapse; background: #fff; border-radius: 12px; overflow: hidden; }
    th, td { padding: 0.85rem; border-bottom: 1px solid #e6edf5; text-align: left; vertical-align: top; }
    th { background: #132238; color: #fff; font-weight: 600; }
    a { color: #0f62fe; text-decoration: none; }
    .badge { display: inline-block; padding: 0.2rem 0.55rem; border-radius: 999px; background: #e6edf5; }
    .muted { color: #5b6b7d; }
  </style>
</head>
<body>
  <h1>Incident List</h1>
  <p class="muted">Operator view untuk Phase 2 candidate action generation.</p>
  <table>
    <thead>
      <tr>
        <th>Title</th>
        <th>Service</th>
        <th>Environment</th>
        <th>Severity</th>
        <th>State</th>
        <th>Created</th>
      </tr>
    </thead>
    <tbody>
      {{if .Incidents}}
        {{range .Incidents}}
        <tr>
          <td><a href="/ui/incidents/{{.ID}}">{{.Title}}</a></td>
          <td>{{.ServiceName}}</td>
          <td>{{.Environment}}</td>
          <td><span class="badge">{{.Severity}}</span></td>
          <td>{{.State}}</td>
          <td>{{formatTime .CreatedAt}}</td>
        </tr>
        {{end}}
      {{else}}
        <tr>
          <td colspan="6">Belum ada incident yang masuk.</td>
        </tr>
      {{end}}
    </tbody>
  </table>
</body>
</html>
`))

var incidentDetailTemplate = template.Must(template.New("incident-detail").Funcs(templateFuncs).Parse(`
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>Triovexa Incident Detail</title>
  <style>
    body { font-family: Segoe UI, sans-serif; margin: 2rem; background: #f7f9fc; color: #132238; }
    a { color: #0f62fe; text-decoration: none; }
    .grid { display: grid; gap: 1rem; grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); }
    .card { background: #fff; border-radius: 14px; padding: 1rem 1.15rem; box-shadow: 0 10px 35px rgba(19,34,56,0.08); }
    .muted { color: #5b6b7d; }
    .pill { display: inline-block; padding: 0.2rem 0.55rem; border-radius: 999px; background: #e6edf5; margin-right: 0.35rem; }
    ul { padding-left: 1.2rem; }
    table { width: 100%; border-collapse: collapse; }
    th, td { padding: 0.65rem; border-bottom: 1px solid #e6edf5; text-align: left; vertical-align: top; }
    th { background: #132238; color: #fff; }
    pre { margin: 0.75rem 0 0 0; padding: 0.75rem; background: #f3f6fb; border-radius: 10px; white-space: pre-wrap; }
  </style>
</head>
<body>
  <p><a href="/ui/incidents">Back to incident list</a></p>
  <h1>{{.Incident.Title}}</h1>
  <p class="muted">{{.Incident.ServiceName}} | {{.Incident.Environment}} | severity {{.Incident.Severity}} | state {{.Incident.State}}</p>

  <div class="grid">
    <section class="card">
      <h2>Summary</h2>
      {{if .Triage}}
        <p>{{.Triage.Summary}}</p>
        <p><strong>Blast Radius:</strong> {{.Triage.BlastRadius}}</p>
        <p><strong>Confidence:</strong> {{.Triage.ConfidenceNotes}}</p>
      {{else}}
        <p>Belum ada triage result.</p>
      {{end}}
    </section>

    <section class="card">
      <h2>Incident Meta</h2>
      <p><strong>ID:</strong> {{.Incident.ID}}</p>
      <p><strong>External Alert ID:</strong> {{.Incident.ExternalAlertID}}</p>
      <p><strong>Alert Source:</strong> {{.Incident.AlertSource}}</p>
      <p><strong>Created:</strong> {{formatTime .Incident.CreatedAt}}</p>
      <p><strong>Updated:</strong> {{formatTime .Incident.UpdatedAt}}</p>
    </section>

    <section class="card">
      <h2>Hypotheses</h2>
      {{if and .Triage .Triage.Hypotheses}}
      <ul>
        {{range .Triage.Hypotheses}}<li>{{.}}</li>{{end}}
      </ul>
      {{else}}
      <p>Belum ada hypotheses.</p>
      {{end}}
    </section>

    <section class="card">
      <h2>Next Steps</h2>
      {{if and .Triage .Triage.NextSteps}}
      <ul>
        {{range .Triage.NextSteps}}<li>{{.}}</li>{{end}}
      </ul>
      {{else}}
      <p>Belum ada suggested next steps.</p>
      {{end}}
    </section>
  </div>

  <section class="card" style="margin-top:1rem;">
    <h2>Candidate Actions</h2>
    <table>
      <thead>
        <tr><th>Action</th><th>Target</th><th>Risk</th><th>Status</th><th>Approval</th><th>Evidence Refs</th></tr>
      </thead>
      <tbody>
        {{if .Actions}}
          {{range .Actions}}
          <tr>
            <td>
              <strong>{{.ActionType}}</strong>
              <p>{{.Rationale}}</p>
              <pre>{{formatJSON .ParametersJSON}}</pre>
            </td>
            <td>{{.TargetResource}}</td>
            <td><span class="pill">{{.RiskLevel}}</span></td>
            <td><span class="pill">{{.Status}}</span></td>
            <td>{{.ApprovalHint}}</td>
            <td>{{joinStrings .EvidenceRefs}}</td>
          </tr>
          {{end}}
        {{else}}
          <tr><td colspan="6">Belum ada candidate action.</td></tr>
        {{end}}
      </tbody>
    </table>
  </section>

  <section class="card" style="margin-top:1rem;">
    <h2>Evidence</h2>
    <table>
      <thead>
        <tr><th>Type</th><th>Source</th><th>Snippet</th><th>Timestamp</th></tr>
      </thead>
      <tbody>
        {{if .Evidence}}
          {{range .Evidence}}
          <tr>
            <td>{{.Type}}</td>
            <td>{{.Source}}</td>
            <td>{{.Snippet}}</td>
            <td>{{formatTime .Timestamp}}</td>
          </tr>
          {{end}}
        {{else}}
          <tr><td colspan="4">Belum ada evidence.</td></tr>
        {{end}}
      </tbody>
    </table>
  </section>

  <section class="card" style="margin-top:1rem;">
    <h2>Relevant Docs</h2>
    <table>
      <thead>
        <tr><th>Type</th><th>Title</th><th>Reason</th><th>Snippet</th></tr>
      </thead>
      <tbody>
        {{if .Documents}}
          {{range .Documents}}
          <tr>
            <td>{{.DocumentType}}</td>
            <td>{{.DocumentTitle}}</td>
            <td>{{.RelevanceReason}}</td>
            <td>{{.Snippet}}</td>
          </tr>
          {{end}}
        {{else}}
          <tr><td colspan="4">Belum ada dokumen relevan.</td></tr>
        {{end}}
      </tbody>
    </table>
  </section>

  <section class="card" style="margin-top:1rem;">
    <h2>Audit Trail</h2>
    <table>
      <thead>
        <tr><th>Step</th><th>Status</th><th>Started</th><th>Finished</th></tr>
      </thead>
      <tbody>
        {{if .AuditTrail}}
          {{range .AuditTrail}}
          <tr>
            <td>{{.StepName}}</td>
            <td><span class="pill">{{.Status}}</span></td>
            <td>{{formatTime .StartedAt}}</td>
            <td>{{formatTime .FinishedAt}}</td>
          </tr>
          {{end}}
        {{else}}
          <tr><td colspan="4">Belum ada audit trail.</td></tr>
        {{end}}
      </tbody>
    </table>
  </section>
</body>
</html>
`))

func renderIncidentList(w http.ResponseWriter, data incidentListPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = incidentListTemplate.Execute(w, data)
}

func renderIncidentDetail(w http.ResponseWriter, data incidentDetailPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = incidentDetailTemplate.Execute(w, data)
}
