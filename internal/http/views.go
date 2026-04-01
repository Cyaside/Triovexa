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
  <style>
    :root {
      --ink: #132238;
      --muted: #5b6b7d;
      --paper: #ffffff;
      --mist: #eef3f8;
      --line: #d8e1ec;
      --accent: #0f62fe;
      --accent-soft: #dbe8ff;
      --success: #0f9d58;
      --success-soft: #dff5e9;
      --warning: #8a5a00;
      --warning-soft: #fff1d6;
      --danger: #b3261e;
      --danger-soft: #fde7e5;
      --shadow: 0 18px 50px rgba(19, 34, 56, 0.08);
    }
    * { box-sizing: border-box; }
    body { font-family: Segoe UI, sans-serif; margin: 0; background:
      radial-gradient(circle at top left, rgba(15,98,254,0.10), transparent 32%),
      linear-gradient(180deg, #f5f8fc 0%, #eef2f7 100%);
      color: var(--ink);
    }
    a { color: var(--accent); text-decoration: none; }
    .shell { width: min(1320px, calc(100% - 2rem)); margin: 0 auto; padding: 2rem 0 3rem; }
    .hero { display: grid; grid-template-columns: minmax(0, 1.5fr) minmax(280px, 0.9fr); gap: 1rem; margin-bottom: 1rem; }
    .hero-card, .panel, .stat-card { background: rgba(255,255,255,0.92); border: 1px solid rgba(216,225,236,0.95); border-radius: 20px; box-shadow: var(--shadow); }
    .hero-card { padding: 1.4rem; }
    .eyebrow { font-size: 0.82rem; letter-spacing: 0.12em; text-transform: uppercase; color: var(--muted); margin: 0 0 0.65rem 0; }
    h1 { margin: 0 0 0.7rem 0; font-size: 2.3rem; line-height: 1.05; }
    h2 { margin: 0 0 0.85rem 0; font-size: 1.2rem; }
    h3 { margin: 0 0 0.4rem 0; font-size: 1rem; }
    p { margin: 0.2rem 0 0.7rem 0; }
    .muted { color: var(--muted); }
    .hero-links { display: flex; flex-wrap: wrap; gap: 0.65rem; margin-top: 1rem; }
    .hero-links a { display: inline-flex; align-items: center; gap: 0.35rem; padding: 0.55rem 0.85rem; border-radius: 999px; background: var(--mist); color: var(--ink); }
    .banner { margin: 0 0 1rem 0; padding: 0.9rem 1rem; border-radius: 14px; border: 1px solid; }
    .banner.warning { background: var(--warning-soft); color: var(--warning); border-color: #f2d395; }
    .banner.success { background: var(--success-soft); color: var(--success); border-color: #b7e4c8; }
    .banner.error { background: var(--danger-soft); color: var(--danger); border-color: #f1b5b1; }
    .stats-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); gap: 0.85rem; margin-bottom: 1rem; }
    .stat-card { padding: 1rem; }
    .stat-label { color: var(--muted); font-size: 0.9rem; }
    .stat-value { font-size: 1.9rem; font-weight: 700; margin-top: 0.35rem; }
    .workbench-grid { display: grid; grid-template-columns: minmax(0, 1.25fr) minmax(300px, 0.8fr); gap: 1rem; margin-bottom: 1rem; }
    .panel { padding: 1.15rem; }
    .scenario-grid { display: grid; gap: 0.85rem; }
    .scenario-card { display: grid; gap: 0.65rem; padding: 1rem; border-radius: 16px; background: linear-gradient(180deg, #fff, #f8fbff); border: 1px solid var(--line); }
    .scenario-meta { display: flex; flex-wrap: wrap; gap: 0.45rem; }
    .chip { display: inline-flex; align-items: center; padding: 0.24rem 0.6rem; border-radius: 999px; background: var(--mist); color: var(--ink); font-size: 0.85rem; }
    form { margin: 0; }
    input, textarea, button { font: inherit; }
    button { padding: 0.62rem 0.9rem; border: none; border-radius: 12px; cursor: pointer; }
    .primary-button { background: var(--accent); color: #fff; }
    .secondary-button { background: #1f3b57; color: #fff; }
    table { width: 100%; border-collapse: collapse; background: transparent; }
    th, td { padding: 0.85rem; border-bottom: 1px solid #e6edf5; text-align: left; vertical-align: top; }
    th { background: #132238; color: #fff; font-weight: 600; }
    .table-wrap { overflow-x: auto; border-radius: 16px; border: 1px solid var(--line); background: var(--paper); }
    .row-title { display: grid; gap: 0.3rem; }
    .tight { margin: 0; }
    .control-form { display: grid; gap: 0.6rem; margin-top: 0.8rem; }
    @media (max-width: 980px) {
      .hero, .workbench-grid { grid-template-columns: 1fr; }
      .shell { width: min(100% - 1rem, 1320px); }
    }
  </style>
</head>
<body>
  <div class="shell">
  <section class="hero">
    <div class="hero-card">
      <p class="eyebrow">Triovexa Workbench</p>
      <h1>Heuristic Incident Workbench</h1>
      <p class="muted">UI sederhana untuk memicu demo scenario, membaca triage heuristik, lalu mengikuti approval, execution, verification, dan rollback dari satu tempat.</p>
      <div class="hero-links">
        <a href="/debug/tools">Diagnostics</a>
        <a href="/debug/policies">Policy Catalog</a>
        <a href="/metrics">Metrics</a>
      </div>
    </div>
    <div class="hero-card">
      <p class="eyebrow">Operator Snapshot</p>
      <h2 class="tight">{{if .KillSwitchEnabled}}Kill Switch Active{{else}}Heuristic Flow Ready{{end}}</h2>
      <p class="muted">Gunakan workbench ini untuk melihat apakah rekomendasi heuristik selaras dengan intended flow sebelum kita sambungkan ke provider AI dan observability sungguhan.</p>
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
  <p class="banner warning">Kill switch sedang aktif. Evaluasi dan triage tetap berjalan, tetapi action baru akan diblok oleh policy.</p>
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
      <p class="muted">Trigger skenario demo langsung dari browser untuk melihat bagaimana heuristik membaca evidence dan mengusulkan candidate action.</p>
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
      <p class="muted">Control panel sederhana untuk menguji safety guardrails saat heuristik berjalan.</p>
      <form class="control-form" method="post" action="/ui/admin/kill-switch">
        <input type="hidden" name="redirect" value="/ui/incidents" />
        <input type="hidden" name="enabled" value="{{if .KillSwitchEnabled}}false{{else}}true{{end}}" />
        <button class="secondary-button" type="submit">{{if .KillSwitchEnabled}}Disable Kill Switch{{else}}Enable Kill Switch{{end}}</button>
      </form>
      <p class="muted">Current state: {{if .KillSwitchEnabled}}enabled{{else}}disabled{{end}}</p>
      <p class="muted">Saat kill switch aktif, incident intake dan triage tetap masuk, tetapi approval atau execution baru akan diblok.</p>
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
                <p class="muted">Triage belum tersedia.</p>
              {{end}}
              <p class="muted">Candidate actions: {{.CandidateActionCount}}</p>
            </td>
            <td>
              {{with .PrimaryAction}}
                <strong>{{.ActionType}}</strong>
                <p class="muted">risk {{.RiskLevel}} -> status {{.Status}}</p>
              {{else}}
                <span class="muted">Belum ada candidate action.</span>
              {{end}}
            </td>
            <td>
              {{if .LatestVerificationStatus}}
                <span class="chip">{{.LatestVerificationStatus}}</span>
              {{else}}
                <span class="muted">Belum ada verification</span>
              {{end}}
            </td>
            <td>{{formatTime .Incident.UpdatedAt}}</td>
          </tr>
          {{end}}
        {{else}}
          <tr>
            <td colspan="5">Belum ada incident yang masuk. Gunakan Demo Scenarios di atas untuk mulai mengetes heuristik.</td>
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
  <style>
    :root {
      --ink: #132238;
      --muted: #5b6b7d;
      --paper: #ffffff;
      --mist: #eef3f8;
      --line: #d8e1ec;
      --accent: #0f62fe;
      --success: #0f9d58;
      --success-soft: #dff5e9;
      --warning: #8a5a00;
      --warning-soft: #fff1d6;
      --danger: #b3261e;
      --danger-soft: #fde7e5;
      --shadow: 0 18px 50px rgba(19,34,56,0.08);
    }
    * { box-sizing: border-box; }
    body { font-family: Segoe UI, sans-serif; margin: 0; background:
      radial-gradient(circle at top right, rgba(15,98,254,0.08), transparent 28%),
      linear-gradient(180deg, #f5f8fc 0%, #eef2f7 100%);
      color: var(--ink);
    }
    .shell { width: min(1320px, calc(100% - 2rem)); margin: 0 auto; padding: 2rem 0 3rem; }
    a { color: #0f62fe; text-decoration: none; }
    .grid { display: grid; gap: 1rem; grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); }
    .card { background: rgba(255,255,255,0.94); border: 1px solid rgba(216,225,236,0.95); border-radius: 18px; padding: 1rem 1.15rem; box-shadow: var(--shadow); }
    .muted { color: #5b6b7d; }
    .pill { display: inline-block; padding: 0.2rem 0.55rem; border-radius: 999px; background: #e6edf5; margin-right: 0.35rem; }
    .banner { margin: 0 0 1rem 0; padding: 0.85rem 1rem; border-radius: 12px; background: var(--warning-soft); color: var(--warning); border: 1px solid #f2d395; }
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
    .hero { display: grid; grid-template-columns: minmax(0, 1.3fr) minmax(290px, 0.8fr); gap: 1rem; margin-bottom: 1rem; }
    .hero h1 { margin: 0 0 0.5rem 0; font-size: 2.25rem; line-height: 1.05; }
    .hero-actions { display: flex; flex-wrap: wrap; gap: 0.5rem; }
    .action-grid { display: grid; gap: 1rem; }
    .action-card { border: 1px solid var(--line); border-radius: 18px; padding: 1rem; background: linear-gradient(180deg, #fff, #f8fbff); }
    .action-header { display: flex; flex-wrap: wrap; justify-content: space-between; gap: 0.75rem; margin-bottom: 0.6rem; }
    .action-columns { display: grid; gap: 1rem; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); margin-top: 0.9rem; }
    .action-block { padding: 0.9rem; background: rgba(238,243,248,0.72); border-radius: 14px; }
    .action-block h3 { margin: 0 0 0.45rem 0; font-size: 0.98rem; }
    details.card { padding: 0; overflow: hidden; }
    details.card summary { cursor: pointer; list-style: none; padding: 1rem 1.15rem; font-weight: 600; }
    details.card summary::-webkit-details-marker { display: none; }
    details.card .details-body { padding: 0 1.15rem 1.1rem; }
    @media (max-width: 980px) {
      .hero { grid-template-columns: 1fr; }
      .shell { width: min(100% - 1rem, 1320px); }
    }
  </style>
</head>
<body>
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
      <p class="muted">Gunakan action lane di bawah untuk menilai apakah hasil heuristik ini cukup aman untuk dieksekusi atau justru perlu dihentikan dan dieskalasi.</p>
    </article>
  </section>
  {{if .Notice}}
  <p class="banner">{{.Notice}}</p>
  {{end}}
  {{if .Error}}
  <p class="banner" style="background:#fde7e5;color:#b3261e;border-color:#f1b5b1;">{{.Error}}</p>
  {{end}}
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
    <h2>Action Lane</h2>
    <p class="muted">Setiap card di bawah merangkum hasil heuristik, policy decision, operator control, dan outcome terakhir untuk satu candidate action.</p>
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
                <p class="muted">Belum ada policy decision.</p>
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
                <p class="muted">Tidak ada aksi approval atau execution manual untuk status saat ini.</p>
                {{end}}
              {{end}}
            </section>

            <section class="action-block">
              <h3>Latest Outcome</h3>
              {{with latestExecutionForAction .ID $.ExecutionRecords}}
                <p><strong>Execution:</strong> <span class="pill">{{.Status}}</span></p>
                <p class="muted">Started {{formatTime .StartedAt}} by {{.InitiatedBy}}</p>
              {{else}}
                <p class="muted">Belum ada execution record.</p>
              {{end}}
              {{with latestVerificationForAction .ID $.VerificationResults $.ExecutionRecords}}
                <p><strong>Verification:</strong> <span class="pill">{{.Status}}</span></p>
                <p>{{.Notes}}</p>
              {{else}}
                <p class="muted">Belum ada verification result.</p>
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
          <tr><td colspan="5">Belum ada approval record.</td></tr>
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
          <tr><td colspan="6">Belum ada execution record.</td></tr>
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
          <tr><td colspan="5">Belum ada verification result.</td></tr>
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
          <tr><td colspan="7">Belum ada rollback record.</td></tr>
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
          <tr><td colspan="4">Belum ada evidence.</td></tr>
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
          <tr><td colspan="4">Belum ada dokumen relevan.</td></tr>
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
          <tr><td colspan="4">Belum ada audit trail.</td></tr>
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
