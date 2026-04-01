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
	Incidents         []domain.Incident
	KillSwitchEnabled bool
}

type incidentDetailPageData struct {
	Incident          domain.Incident
	Triage            *domain.TriageResult
	Evidence          []domain.EvidenceItem
	Documents         []domain.DocumentReference
	Actions           []domain.CandidateAction
	PolicyDecisions   []domain.PolicyDecision
	ApprovalRecords   []domain.ApprovalRecord
	ExecutionRecords  []domain.ExecutionRecord
	KillSwitchEnabled bool
	AuditTrail        []domain.AuditEvent
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
	"policyFor": func(actionID string, decisions []domain.PolicyDecision) *domain.PolicyDecision {
		for _, decision := range decisions {
			if decision.CandidateActionID == actionID {
				copy := decision
				return &copy
			}
		}
		return nil
	},
	"executionsFor": func(actionID string, records []domain.ExecutionRecord) []domain.ExecutionRecord {
		filtered := make([]domain.ExecutionRecord, 0)
		for _, record := range records {
			if record.CandidateActionID == actionID {
				filtered = append(filtered, record)
			}
		}
		return filtered
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
    .banner { margin: 0 0 1rem 0; padding: 0.85rem 1rem; border-radius: 12px; background: #fff1d6; color: #8a5a00; border: 1px solid #f2d395; }
  </style>
</head>
<body>
  <h1>Incident List</h1>
  <p class="muted">Operator view untuk Phase 4 low-risk execution MVP.</p>
  {{if .KillSwitchEnabled}}
  <p class="banner">Kill switch sedang aktif. Evaluasi dan triage tetap berjalan, tetapi action baru akan diblok oleh policy.</p>
  {{end}}
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
    .banner { margin: 0 0 1rem 0; padding: 0.85rem 1rem; border-radius: 12px; background: #fff1d6; color: #8a5a00; border: 1px solid #f2d395; }
    ul { padding-left: 1.2rem; }
    table { width: 100%; border-collapse: collapse; }
    th, td { padding: 0.65rem; border-bottom: 1px solid #e6edf5; text-align: left; vertical-align: top; }
    th { background: #132238; color: #fff; }
    pre { margin: 0.75rem 0 0 0; padding: 0.75rem; background: #f3f6fb; border-radius: 10px; white-space: pre-wrap; }
    form { margin-top: 0.75rem; display: grid; gap: 0.5rem; }
    input, textarea, button { font: inherit; }
    input, textarea { width: 100%; padding: 0.55rem; border: 1px solid #d5dde8; border-radius: 8px; }
    button { padding: 0.55rem 0.75rem; border: none; border-radius: 8px; cursor: pointer; }
    .approve { background: #0f9d58; color: #fff; }
    .reject { background: #c5221f; color: #fff; }
  </style>
</head>
<body>
  <p><a href="/ui/incidents">Back to incident list</a></p>
  <h1>{{.Incident.Title}}</h1>
  <p class="muted">{{.Incident.ServiceName}} | {{.Incident.Environment}} | severity {{.Incident.Severity}} | state {{.Incident.State}}</p>
  {{if .KillSwitchEnabled}}
  <p class="banner">Kill switch sedang aktif. Action baru akan ditolak oleh policy evaluator sampai dinonaktifkan kembali.</p>
  {{end}}

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
        <tr><th>Action</th><th>Target</th><th>Risk</th><th>Status</th><th>Policy Decision</th><th>Approval</th><th>Evidence Refs</th><th>Controls</th></tr>
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
            <td>
              {{with policyFor .ID $.PolicyDecisions}}
                <strong>{{.Decision}}</strong>
                <p>{{.Reason}}</p>
                <p><code>{{.PolicyRuleRef}}</code></p>
              {{else}}
                <p>Belum ada policy decision.</p>
              {{end}}
            </td>
            <td>{{.ApprovalHint}}</td>
            <td>{{joinStrings .EvidenceRefs}}</td>
            <td>
              {{if eq .Status "awaiting_approval"}}
              <form method="post" action="/actions/{{.ID}}/approve">
                <input type="text" name="approved_by" placeholder="operator name" />
                <textarea name="note" rows="2" placeholder="approval note"></textarea>
                <button class="approve" type="submit">Approve</button>
              </form>
              <form method="post" action="/actions/{{.ID}}/reject">
                <input type="text" name="approved_by" placeholder="operator name" />
                <textarea name="note" rows="2" placeholder="rejection note"></textarea>
                <button class="reject" type="submit">Reject</button>
              </form>
              {{else}}
                {{if and (or (eq .Status "approved") (eq .Status "allowed")) (not $.KillSwitchEnabled)}}
                <form method="post" action="/actions/{{.ID}}/execute">
                  <input type="text" name="initiated_by" placeholder="operator name" />
                  <textarea name="note" rows="2" placeholder="execution note"></textarea>
                  <button class="approve" type="submit">Execute</button>
                </form>
                {{else}}
                <p>Tidak ada aksi approval atau execution manual.</p>
                {{end}}
              {{end}}
            </td>
          </tr>
          {{end}}
        {{else}}
          <tr><td colspan="8">Belum ada candidate action.</td></tr>
        {{end}}
      </tbody>
    </table>
  </section>

  <section class="card" style="margin-top:1rem;">
    <h2>Approval Records</h2>
    <table>
      <thead>
        <tr><th>Action ID</th><th>Decision</th><th>Approved By</th><th>Note</th><th>Created</th></tr>
      </thead>
      <tbody>
        {{if .ApprovalRecords}}
          {{range .ApprovalRecords}}
          <tr>
            <td>{{.CandidateActionID}}</td>
            <td><span class="pill">{{.Decision}}</span></td>
            <td>{{.ApprovedBy}}</td>
            <td>{{.Note}}</td>
            <td>{{formatTime .CreatedAt}}</td>
          </tr>
          {{end}}
        {{else}}
          <tr><td colspan="5">Belum ada approval record.</td></tr>
        {{end}}
      </tbody>
    </table>
  </section>

  <section class="card" style="margin-top:1rem;">
    <h2>Execution Records</h2>
    <table>
      <thead>
        <tr><th>Action ID</th><th>Status</th><th>Executor</th><th>Initiated By</th><th>Started</th><th>Finished</th></tr>
      </thead>
      <tbody>
        {{if .ExecutionRecords}}
          {{range .ExecutionRecords}}
          <tr>
            <td>{{.CandidateActionID}}</td>
            <td><span class="pill">{{.Status}}</span></td>
            <td>{{.ExecutorType}}</td>
            <td>{{.InitiatedBy}}</td>
            <td>{{formatTime .StartedAt}}</td>
            <td>{{formatTime .FinishedAt}}</td>
          </tr>
          {{end}}
        {{else}}
          <tr><td colspan="6">Belum ada execution record.</td></tr>
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
