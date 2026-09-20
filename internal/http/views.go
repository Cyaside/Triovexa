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
	Items             []incidentListItem
	KillSwitchEnabled bool
	Stats             incidentDashboardStats
	DemoScenarios     []demoScenarioView
	Runtime           runtimeViewData
	Notice            string
	Error             string
}

type incidentListItem struct {
	Incident                 domain.Incident
	CandidateActionCount     int
	PrimaryAction            *domain.CandidateAction
	TriageSummary            string
	LatestVerificationStatus string
}

type incidentDashboardStats struct {
	Total            int
	AwaitingApproval int
	InFlight         int
	Resolved         int
	RolledBack       int
	Escalated        int
}

type demoScenarioView struct {
	Key            string
	Name           string
	Summary        string
	HeuristicFocus string
	ExpectedAction string
}

type incidentDetailPageData struct {
	Incident            domain.Incident
	Triage              *domain.TriageResult
	Evidence            []domain.EvidenceItem
	Documents           []domain.DocumentReference
	Actions             []domain.CandidateAction
	PolicyDecisions     []domain.PolicyDecision
	ApprovalRecords     []domain.ApprovalRecord
	ExecutionRecords    []domain.ExecutionRecord
	VerificationResults []domain.VerificationResult
	RollbackRecords     []domain.RollbackRecord
	KillSwitchEnabled   bool
	AuditTrail          []domain.AuditEvent
	Runtime             runtimeViewData
	Notice              string
	Error               string
	NextOperatorStep    string
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
	"replaceUnderscores": func(value string) string {
		if strings.TrimSpace(value) == "" {
			return "-"
		}
		return strings.ReplaceAll(value, "_", " ")
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
	"executionFor": func(executionRecordID string, records []domain.ExecutionRecord) *domain.ExecutionRecord {
		for _, record := range records {
			if record.ID == executionRecordID {
				copy := record
				return &copy
			}
		}
		return nil
	},
	"latestExecutionForAction": func(actionID string, records []domain.ExecutionRecord) *domain.ExecutionRecord {
		var latest *domain.ExecutionRecord
		for _, record := range records {
			if record.CandidateActionID != actionID {
				continue
			}
			copy := record
			if latest == nil || copy.StartedAt.After(latest.StartedAt) {
				latest = &copy
			}
		}
		return latest
	},
	"latestVerificationForAction": func(actionID string, verificationResults []domain.VerificationResult, executionRecords []domain.ExecutionRecord) *domain.VerificationResult {
		executionIDs := make(map[string]struct{})
		for _, record := range executionRecords {
			if record.CandidateActionID == actionID {
				executionIDs[record.ID] = struct{}{}
			}
		}

		var latest *domain.VerificationResult
		for _, result := range verificationResults {
			if _, ok := executionIDs[result.ExecutionRecordID]; !ok {
				continue
			}
			copy := result
			if latest == nil || copy.CreatedAt.After(latest.CreatedAt) {
				latest = &copy
			}
		}
		return latest
	},
	"rollbacksFor": func(actionID string, records []domain.RollbackRecord) []domain.RollbackRecord {
		filtered := make([]domain.RollbackRecord, 0)
		for _, record := range records {
			if record.CandidateActionID == actionID {
				filtered = append(filtered, record)
			}
		}
		return filtered
	},
	"latestRollbackForAction": func(actionID string, records []domain.RollbackRecord) *domain.RollbackRecord {
		var latest *domain.RollbackRecord
		for _, record := range records {
			if record.CandidateActionID != actionID {
				continue
			}
			copy := record
			if latest == nil || copy.StartedAt.After(latest.StartedAt) {
				latest = &copy
			}
		}
		return latest
	},
}

var incidentListTemplate = template.Must(template.New("incident-list").Funcs(templateFuncs).Parse(`
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>Triovexa Incident List</title>
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="stylesheet" href="/ui/assets/workbench.css">
</head>
<body class="page-list">
  <div class="shell">
  <section class="hero">
    <div class="hero-card">
      <p class="eyebrow">Triovexa Workbench</p>
      <h1>Incident Workbench</h1>
      <p class="muted">Use one workspace to trigger demo scenarios, review AI or heuristic triage, and follow approval, execution, verification, and rollback.</p>
      <div class="hero-links">
        <a href="/debug/tools">Diagnostics</a>
        <a href="/debug/policies">Policy Catalog</a>
        <a href="/metrics">Metrics</a>
        <a href="/ui/setup/observability">Observability Setup</a>
      </div>
    </div>
    <div class="hero-card">
      <p class="eyebrow">Operator Snapshot</p>
      <h2 class="tight">{{if .KillSwitchEnabled}}Kill Switch Active{{else}}Runtime Flow Ready{{end}}</h2>
      <p class="muted">Switch between heuristic and configured providers, then compare triage and action generation without restarting the server.</p>
      <p><span class="chip">Reasoning {{.Runtime.ReasoningMode}}</span> <span class="chip">Observability {{.Runtime.ObservabilityMode}}</span></p>
      <p><span class="chip">Total incident {{.Stats.Total}}</span> <span class="chip">Awaiting approval {{.Stats.AwaitingApproval}}</span></p>
    </div>
  </section>
  {{if .Notice}}
  <p class="banner success">{{.Notice}}</p>
  {{end}}
  {{if .Error}}
  <p class="banner error">{{.Error}}</p>
  {{end}}
  {{if .KillSwitchEnabled}}
  <p class="banner warning">The kill switch is active. Evaluation and triage continue, but policy blocks new actions.</p>
  {{end}}
  <section class="stats-grid">
    <article class="stat-card"><div class="stat-label">Total Incident</div><div class="stat-value">{{.Stats.Total}}</div></article>
    <article class="stat-card"><div class="stat-label">Awaiting Approval</div><div class="stat-value">{{.Stats.AwaitingApproval}}</div></article>
    <article class="stat-card"><div class="stat-label">In Flight</div><div class="stat-value">{{.Stats.InFlight}}</div></article>
    <article class="stat-card"><div class="stat-label">Resolved</div><div class="stat-value">{{.Stats.Resolved}}</div></article>
    <article class="stat-card"><div class="stat-label">Rolled Back</div><div class="stat-value">{{.Stats.RolledBack}}</div></article>
    <article class="stat-card"><div class="stat-label">Escalated</div><div class="stat-value">{{.Stats.Escalated}}</div></article>
  </section>

  <section class="workbench-grid">
    <article class="panel">
      <h2>Demo Scenarios</h2>
      <p class="muted">Trigger a demo scenario from the browser to see how the active reasoning mode reads evidence and proposes candidate actions.</p>
      <div class="scenario-grid">
        {{range .DemoScenarios}}
        <form class="scenario-card" method="post" action="/ui/demo/scenarios/{{.Key}}">
          <div>
            <h3>{{.Name}}</h3>
            <p class="muted">{{.Summary}}</p>
          </div>
          <div class="scenario-meta">
            <span class="chip">Heuristic focus: {{.HeuristicFocus}}</span>
            <span class="chip">Expected action: {{.ExpectedAction}}</span>
          </div>
          <button class="primary-button" type="submit">Trigger Scenario</button>
        </form>
        {{end}}
      </div>
    </article>

    <article class="panel">
      <h2>Operator Controls</h2>
      <p class="muted">Use these controls to exercise safety guardrails and change the runtime provider without a restart.</p>
      <form class="control-form" method="post" action="/ui/admin/kill-switch">
        <input type="hidden" name="redirect" value="/ui/incidents" />
        <input type="hidden" name="enabled" value="{{if .KillSwitchEnabled}}false{{else}}true{{end}}" />
        <button class="secondary-button" type="submit">{{if .KillSwitchEnabled}}Disable Kill Switch{{else}}Enable Kill Switch{{end}}</button>
      </form>
      <form class="control-form" method="post" action="/ui/admin/runtime-modes">
        <input type="hidden" name="redirect" value="/ui/incidents" />
        <input type="hidden" name="reasoning_mode" value="{{if eq .Runtime.ReasoningMode "mistral"}}heuristic{{else}}mistral{{end}}" />
        <button class="secondary-button" type="submit">Switch Reasoning to {{if eq .Runtime.ReasoningMode "mistral"}}heuristic{{else}}mistral{{end}}</button>
      </form>
      <form class="control-form" method="post" action="/ui/admin/runtime-modes">
        <input type="hidden" name="redirect" value="/ui/incidents" />
        <input type="hidden" name="observability_mode" value="{{if eq .Runtime.ObservabilityMode "grafana"}}demo{{else}}grafana{{end}}" />
        <button class="secondary-button" type="submit">Switch Observability to {{if eq .Runtime.ObservabilityMode "grafana"}}demo{{else}}grafana{{end}}</button>
      </form>
      <p class="muted">Current state: {{if .KillSwitchEnabled}}enabled{{else}}disabled{{end}}</p>
      <p class="muted">When the kill switch is active, incident intake and triage continue, but new approvals and executions are blocked.</p>
      <p class="muted">Mistral: {{if .Runtime.MistralConfigured}}configured ({{.Runtime.MistralModel}}){{else}}not configured{{end}}</p>
      <p class="muted">Grafana: {{if .Runtime.GrafanaConfigured}}configured{{else}}not configured{{end}} | metrics UID {{if .Runtime.MetricsSourceUID}}{{.Runtime.MetricsSourceUID}}{{else}}-{{end}} | logs UID {{if .Runtime.LogsSourceUID}}{{.Runtime.LogsSourceUID}}{{else}}-{{end}}</p>
      {{if .Runtime.DatasourceCount}}
      <p class="muted">Datasource discovery: {{.Runtime.DatasourceCount}} source(s) visible from Grafana.</p>
      {{end}}
      {{if .Runtime.DatasourceError}}
      <p class="muted">Datasource discovery error: {{.Runtime.DatasourceError}}</p>
      {{end}}
      <p><a href="/ui/setup/observability">Open observability setup page</a></p>
    </article>
  </section>

  <section class="panel">
    <h2>Incident Workbench</h2>
    <div class="table-wrap">
    <table>
      <thead>
        <tr>
          <th>Incident</th>
          <th>Heuristic Snapshot</th>
          <th>Primary Action</th>
          <th>Latest Outcome</th>
          <th>Updated</th>
        </tr>
      </thead>
      <tbody>
        {{if .Items}}
          {{range .Items}}
          <tr>
            <td>
              <div class="row-title">
                <a href="/ui/incidents/{{.Incident.ID}}"><strong>{{.Incident.Title}}</strong></a>
                <span class="muted">{{.Incident.ServiceName}} | {{.Incident.Environment}} | severity {{.Incident.Severity}}</span>
                <span class="chip">{{.Incident.State}}</span>
              </div>
            </td>
            <td>
              {{if .TriageSummary}}
                <p>{{.TriageSummary}}</p>
              {{else}}
                <p class="muted">Triage is not available yet.</p>
              {{end}}
              <p class="muted">Candidate actions: {{.CandidateActionCount}}</p>
            </td>
            <td>
              {{with .PrimaryAction}}
                <strong>{{.ActionType}}</strong>
                <p class="muted">risk {{.RiskLevel}} -> status {{.Status}}</p>
              {{else}}
                <span class="muted">No candidate actions yet.</span>
              {{end}}
            </td>
            <td>
              {{if .LatestVerificationStatus}}
                <span class="chip">{{.LatestVerificationStatus}}</span>
              {{else}}
                <span class="muted">No verification yet.</span>
              {{end}}
            </td>
            <td>{{formatTime .Incident.UpdatedAt}}</td>
          </tr>
          {{end}}
        {{else}}
          <tr>
            <td colspan="5">No incidents have arrived. Use Demo Scenarios above to exercise the heuristic workflow.</td>
          </tr>
        {{end}}
      </tbody>
    </table>
    </div>
  </section>
  </div>
</body>
</html>
`))

var incidentDetailTemplate = template.Must(template.New("incident-detail").Funcs(templateFuncs).Parse(`
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>Triovexa Incident Detail</title>
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="stylesheet" href="/ui/assets/workbench.css">
</head>
<body class="page-detail">
  <div class="shell">
  <p><a href="/ui/incidents">Back to incident list</a></p>
  <section class="hero">
    <article class="card">
      <h1>{{.Incident.Title}}</h1>
      <p class="muted">{{.Incident.ServiceName}} | {{.Incident.Environment}} | severity {{.Incident.Severity}} | state {{.Incident.State}}</p>
      <div class="hero-actions">
        <span class="pill">{{.Incident.State}}</span>
        <span class="pill">{{.Incident.Environment}}</span>
        <span class="pill">{{.Incident.Severity}}</span>
      </div>
    </article>
    <article class="card">
      <h2>Operator Step</h2>
      <p>{{.NextOperatorStep}}</p>
      <p class="muted">Use the action lane below to decide whether the active reasoning result is safe to execute or should be stopped and escalated.</p>
      <p class="muted">Reasoning mode: {{.Runtime.ReasoningMode}} | Observability mode: {{.Runtime.ObservabilityMode}}</p>
    </article>
  </section>
  {{if .Notice}}
  <p class="banner">{{.Notice}}</p>
  {{end}}
  {{if .Error}}
  <p class="banner" style="background:#fde7e5;color:#b3261e;border-color:#f1b5b1;">{{.Error}}</p>
  {{end}}
  {{if .KillSwitchEnabled}}
  <p class="banner">The kill switch is active. The policy evaluator rejects new actions until it is disabled.</p>
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
    <h2>Action Lane</h2>
    <p class="muted">Each entry below summarizes the heuristic result, policy decision, operator controls, and latest outcome for one candidate action.</p>
    <div class="action-grid">
      {{if .Actions}}
        {{range .Actions}}
        <article class="action-card">
          <div class="action-header">
            <div>
              <h3>{{replaceUnderscores .ActionType}}</h3>
              <p class="muted">{{.TargetResource}}</p>
            </div>
            <div>
              <span class="pill">{{.RiskLevel}}</span>
              <span class="pill">{{.Status}}</span>
            </div>
          </div>
          <p>{{.Rationale}}</p>
          <div class="action-columns">
            <section class="action-block">
              <h3>Policy</h3>
              {{with policyFor .ID $.PolicyDecisions}}
                <p><strong>{{.Decision}}</strong></p>
                <p>{{.Reason}}</p>
                <p class="muted"><code>{{.PolicyRuleRef}}</code></p>
              {{else}}
                <p class="muted">No policy decision yet.</p>
              {{end}}
              <p><strong>Approval Hint:</strong> {{.ApprovalHint}}</p>
              <p><strong>Evidence Refs:</strong> {{joinStrings .EvidenceRefs}}</p>
            </section>

            <section class="action-block">
              <h3>Parameters</h3>
              <pre>{{formatJSON .ParametersJSON}}</pre>
            </section>

            <section class="action-block">
              <h3>Operator Controls</h3>
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
                <p class="muted">No manual approval or execution action is available for the current state.</p>
                {{end}}
              {{end}}
            </section>

            <section class="action-block">
              <h3>Latest Outcome</h3>
              {{with latestExecutionForAction .ID $.ExecutionRecords}}
                <p><strong>Execution:</strong> <span class="pill">{{.Status}}</span></p>
                <p class="muted">Started {{formatTime .StartedAt}} by {{.InitiatedBy}}</p>
              {{else}}
                <p class="muted">No execution record yet.</p>
              {{end}}
              {{with latestVerificationForAction .ID $.VerificationResults $.ExecutionRecords}}
                <p><strong>Verification:</strong> <span class="pill">{{.Status}}</span></p>
                <p>{{.Notes}}</p>
              {{else}}
                <p class="muted">No verification result yet.</p>
              {{end}}
              {{with latestRollbackForAction .ID $.RollbackRecords}}
                <p><strong>Rollback:</strong> <span class="pill">{{.Status}}</span> via {{replaceUnderscores .RollbackActionKey}}</p>
                <p class="muted">Triggered by {{.TriggeredBy}}</p>
              {{end}}
            </section>
          </div>
        </article>
        {{end}}
      {{else}}
        <p>Belum ada candidate action.</p>
      {{end}}
    </div>
  </section>

  <details class="card" open style="margin-top:1rem;">
    <summary>Approval Records</summary>
    <div class="details-body">
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
          <tr><td colspan="5">No approval records yet.</td></tr>
        {{end}}
      </tbody>
    </table>
    </div>
  </details>

  <details class="card" open style="margin-top:1rem;">
    <summary>Execution Records</summary>
    <div class="details-body">
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
          <tr><td colspan="6">No execution records yet.</td></tr>
        {{end}}
      </tbody>
    </table>
    </div>
  </details>

  <details class="card" style="margin-top:1rem;">
    <summary>Verification Results</summary>
    <div class="details-body">
    <h2>Verification Results</h2>
    <table>
      <thead>
        <tr><th>Action ID</th><th>Execution ID</th><th>Status</th><th>Created</th><th>Notes</th></tr>
      </thead>
      <tbody>
        {{if .VerificationResults}}
          {{range .VerificationResults}}
          <tr>
            <td>{{with executionFor .ExecutionRecordID $.ExecutionRecords}}{{.CandidateActionID}}{{else}}-{{end}}</td>
            <td>{{.ExecutionRecordID}}</td>
            <td><span class="pill">{{.Status}}</span></td>
            <td>{{formatTime .CreatedAt}}</td>
            <td>
              <p>{{.Notes}}</p>
              <pre>{{formatJSON .EvidenceJSON}}</pre>
            </td>
          </tr>
          {{end}}
        {{else}}
          <tr><td colspan="5">No verification results yet.</td></tr>
        {{end}}
      </tbody>
    </table>
    </div>
  </details>

  <details class="card" style="margin-top:1rem;">
    <summary>Rollback Records</summary>
    <div class="details-body">
    <h2>Rollback Records</h2>
    <table>
      <thead>
        <tr><th>Action ID</th><th>Rollback Action</th><th>Status</th><th>Triggered By</th><th>Started</th><th>Finished</th><th>Notes</th></tr>
      </thead>
      <tbody>
        {{if .RollbackRecords}}
          {{range .RollbackRecords}}
          <tr>
            <td>{{.CandidateActionID}}</td>
            <td>{{.RollbackActionKey}}</td>
            <td><span class="pill">{{.Status}}</span></td>
            <td>{{.TriggeredBy}}</td>
            <td>{{formatTime .StartedAt}}</td>
            <td>{{formatTime .FinishedAt}}</td>
            <td>
              <p>{{.Note}}</p>
              <pre>{{formatJSON .ResultJSON}}</pre>
            </td>
          </tr>
          {{end}}
        {{else}}
          <tr><td colspan="7">No rollback records yet.</td></tr>
        {{end}}
      </tbody>
    </table>
    </div>
  </details>

  <details class="card" open style="margin-top:1rem;">
    <summary>Evidence</summary>
    <div class="details-body">
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
          <tr><td colspan="4">No evidence yet.</td></tr>
        {{end}}
      </tbody>
    </table>
    </div>
  </details>

  <details class="card" style="margin-top:1rem;">
    <summary>Relevant Docs</summary>
    <div class="details-body">
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
          <tr><td colspan="4">No relevant documents yet.</td></tr>
        {{end}}
      </tbody>
    </table>
    </div>
  </details>

  <details class="card" style="margin-top:1rem;">
    <summary>Audit Trail</summary>
    <div class="details-body">
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
          <tr><td colspan="4">No audit trail yet.</td></tr>
        {{end}}
      </tbody>
    </table>
    </div>
  </details>
  </div>
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
