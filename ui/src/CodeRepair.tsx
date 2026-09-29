import { FormEvent, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, APIError, Incident } from './api'

type User = { id: string; role: string }
type RepairCase = { ID: string; IncidentID: string; BindingID: string; BaseSHA: string; DeployedSHA: string; State: string; Version: number; CreatedBy: string }
type Binding = { ID: string; RepositoryURL: string; BaseRef: string; AllowedPaths: string[]; TestRecipes: string[]; PolicyVersion: string }
type Evidence = { captured_at: string; sha256: string; entries: { id: string; type: string; source: string; status: string; text: string }[] }
type Attempt = { ID: string; Status: string; Provider: string; Model: string; ErrorCode: string; ErrorMessage: string }
type Report = { status: string; hypothesis?: string; recipe_id: string; before_exit: number; after_exit: number; patch_sha256?: string; evidence_ids?: string[]; code?: string; reason?: string }
type Publication = { State: string; BranchName: string; PatchSHA256: string; HeadSHA: string; MergeSHA: string; PRNumber: number; PRURL: string; LastError: string }
type Deployment = { DeploymentID: string; Environment: string; RevisionSHA: string; Phase: string; VerificationStatus: string; Baseline: { QueueBacklog: number; JobsProcessed: number } }
type Event = { ID: string; Type: string; ActorID: string; DetailsJSON: string; CreatedAt: string }
type CaseDetail = { case: RepairCase; binding: Binding; evidence: Evidence; attempt?: Attempt; report?: Report; patch?: string; review_digest?: string; publish_approval?: { ActorID: string; ExpiresAt: string }; publication?: Publication; deployments: Deployment[]; events: Event[] }

function label(value: string) { return value.replaceAll('_', ' ') }
function shortSHA(value: string) { return value ? value.slice(0, 12) : '—' }

export function CodeRepairPanel({ incident, user }: { incident: Incident; user: User }) {
  const client = useQueryClient()
  const [notice, setNotice] = useState('')
  const [selectedCaseID, setSelectedCaseID] = useState('')
  const cases = useQuery({ queryKey: ['repair', 'incident', incident.ID], queryFn: () => api<{ items: RepairCase[] }>(`/api/v1/repair/incidents/${incident.ID}`), refetchInterval: document.visibilityState === 'visible' ? 5000 : false })
  const latest = cases.data?.items[0]
  const current = cases.data?.items.find((item) => item.ID === selectedCaseID) ?? latest
  const canOpenNew = !latest || ['recovered', 'failed', 'cancelled', 'closed_without_merge'].includes(latest.State)
  const binding = useQuery({ queryKey: ['repair', 'binding', incident.ServiceName, incident.Environment], queryFn: () => api<Binding>(`/api/v1/repair/bindings?service=${encodeURIComponent(incident.ServiceName)}&environment=${encodeURIComponent(incident.Environment)}`), enabled: canOpenNew, retry: false })
  const propose = useMutation({ mutationFn: () => api<RepairCase>(`/api/v1/repair/incidents/${incident.ID}`, { method: 'POST', body: '{}' }), onSuccess: () => { setNotice('Investigation proposal created. Review the scope before approving.'); client.invalidateQueries({ queryKey: ['repair', 'incident', incident.ID] }) } })
  if (cases.isLoading) return <p className="muted">Loading code repair cases…</p>
  if (cases.isError) return <RepairError error={cases.error} retry={() => cases.refetch()} />
  const eligible = incident.State === 'escalated' || incident.State === 'failed_remediation'
  if (current) return <div className="repair-content">
    {cases.data && cases.data.items.length > 1 && <label className="repair-case-picker">Repair case
      <select value={current.ID} onChange={(event) => setSelectedCaseID(event.target.value)}>{cases.data.items.map((item) => <option key={item.ID} value={item.ID}>{item.ID.slice(0, 8)} · {label(item.State)}</option>)}</select>
    </label>}
    {canOpenNew && eligible && binding.data && user.role !== 'viewer' && <button className="quiet-button" disabled={propose.isPending} onClick={() => { setSelectedCaseID(''); propose.mutate() }}>Start another investigation</button>}
    {propose.isError && <RepairError error={propose.error} retry={() => propose.reset()} />}
    {notice && <p className="repair-note" role="status">{notice}</p>}
    <RepairCaseView caseID={current.ID} user={user} />
  </div>
  return <section className="repair-content">
    <h2>Code repair</h2><p className="muted">Investigate a source defect only when an operational action did not resolve this incident. An operator must approve repository access and later approve the exact patch before a draft PR is created.</p>
    {!eligible && <p className="repair-note">This incident is not escalated or marked as failed remediation. Code investigation is unavailable.</p>}
    {binding.isError && binding.error instanceof APIError && binding.error.status === 404 && user.role === 'admin' && <BindingForm incident={incident} onCreated={() => binding.refetch()} />}
    {binding.data && <dl className="repair-facts"><div><dt>Repository</dt><dd>{binding.data.RepositoryURL}</dd></div><div><dt>Base branch</dt><dd><code>{binding.data.BaseRef}</code></dd></div><div><dt>Allowed paths</dt><dd>{binding.data.AllowedPaths.join(', ')}</dd></div></dl>}
    {binding.isError && !(binding.error instanceof APIError && binding.error.status === 404) && <RepairError error={binding.error} retry={() => binding.refetch()} />}
    {notice && <p className="repair-note" role="status">{notice}</p>}
    {propose.isError && <RepairError error={propose.error} retry={() => propose.reset()} />}
    {eligible && binding.data && user.role !== 'viewer' && <button className="primary" disabled={propose.isPending} onClick={() => propose.mutate()}>{propose.isPending ? 'Checking evidence…' : 'Request code investigation'}</button>}
  </section>
}

function BindingForm({ incident, onCreated }: { incident: Incident; onCreated: () => void }) {
  const create = useMutation({ mutationFn: (body: object) => api('/api/v1/repair/bindings', { method: 'POST', body: JSON.stringify(body) }), onSuccess: onCreated })
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const data = new FormData(event.currentTarget)
    create.mutate({ service_name: incident.ServiceName, environment: incident.Environment,
      repository_url: String(data.get('repository_url')).trim(), base_ref: String(data.get('base_ref')).trim(),
      allowed_paths: String(data.get('allowed_paths')).split(',').map((s) => s.trim()).filter(Boolean),
      test_recipes: ['go-test-workload'] })
  }
  return <form className="repair-setup" onSubmit={submit}><h3>Register repository scope</h3><p className="muted">Admin only. The agent can read and modify existing files in the allowed paths, then run the fixed workload regression test.</p>
    <label>GitHub repository URL<input name="repository_url" placeholder="https://github.com/owner/repository" required /></label>
    <label>Protected base branch<input name="base_ref" placeholder="main" required /></label>
    <label>Allowed paths, comma separated<input name="allowed_paths" placeholder="internal/workload" required /></label>
    {create.isError && <RepairError error={create.error} retry={() => create.reset()} />}
    <button className="primary" disabled={create.isPending}>Register repository</button>
  </form>
}

function RepairCaseView({ caseID, user }: { caseID: string; user: User }) {
  const client = useQueryClient()
  const detail = useQuery({ queryKey: ['repair', 'case', caseID], queryFn: () => api<CaseDetail>(`/api/v1/repair/cases/${caseID}`), refetchInterval: document.visibilityState === 'visible' ? 5000 : false })
  const mutation = useMutation({ mutationFn: ({ action, body }: { action: string; body: object }) => api(`/api/v1/repair/cases/${caseID}/${action}`, { method: 'POST', body: JSON.stringify(body) }),
    onSuccess: () => { client.invalidateQueries({ queryKey: ['repair', 'case', caseID] }); client.invalidateQueries({ queryKey: ['repair', 'incident'] }) }, retry: false })
  if (detail.isLoading) return <p className="muted">Loading repair case…</p>
  if (!detail.data) return <RepairError error={detail.error} retry={() => detail.refetch()} />
  const data = detail.data, c = data.case, canAct = user.role !== 'viewer'
  const approvalExpired = data.publish_approval ? new Date(data.publish_approval.ExpiresAt).getTime() < Date.now() : false
  return <div className="repair-content">
    <header className="repair-heading"><div><p className="eyebrow">Code repair · {c.ID.slice(0, 8)}</p><h2>{label(c.State)}</h2></div><span className="repair-state">{label(c.State)}</span></header>
    <p className="muted">A reviewed patch becomes a draft PR. Merge and deployment remain in the repository's normal workflow; recovery is decided from workload telemetry.</p>
    {detail.isRefetchError && <p className="repair-note">This case may be stale. Refresh before acting.</p>}
    {mutation.isError && <RepairError error={mutation.error} retry={() => { mutation.reset(); detail.refetch() }} />}
    <dl className="repair-facts"><div><dt>Repository</dt><dd>{data.binding.RepositoryURL}</dd></div><div><dt>Base</dt><dd><code>{data.binding.BaseRef} · {shortSHA(c.BaseSHA)}</code></dd></div><div><dt>Deployed at investigation</dt><dd><code>{shortSHA(c.DeployedSHA)}</code></dd></div><div><dt>Scope</dt><dd>{data.binding.AllowedPaths.join(', ')}</dd></div></dl>
    <section className="repair-section"><h3>Evidence</h3><p className="muted">Captured {new Date(data.evidence.captured_at).toLocaleString()} · {data.evidence.entries.length} entries</p>
      <div className="repair-list">{data.evidence.entries.map((entry) => <details key={entry.id}><summary><strong>{entry.type}</strong><span>{entry.source}</span><span>{entry.status}</span></summary><pre>{entry.text}</pre></details>)}</div>
    </section>
    {c.State === 'awaiting_investigation_approval' && canAct && <section className="repair-section"><h3>Authorize investigation</h3><p>The runner will inspect this registered repository at the pinned base revision. It cannot execute arbitrary commands or publish a branch.</p><button className="primary" disabled={mutation.isPending} onClick={() => mutation.mutate({ action: 'investigate', body: {} })}>Approve code investigation</button></section>}
    {data.attempt && <section className="repair-section"><h3>Investigation</h3><dl className="repair-facts"><div><dt>Status</dt><dd>{label(data.attempt.Status)}</dd></div><div><dt>Model</dt><dd>{data.attempt.Provider} · {data.attempt.Model}</dd></div></dl>{data.attempt.ErrorCode && <p className="repair-note">{data.attempt.ErrorCode}: {data.attempt.ErrorMessage}</p>}
      {data.report && <><p>{data.report.hypothesis || data.report.reason || 'No diagnosis was recorded.'}</p><dl className="repair-facts"><div><dt>Regression recipe</dt><dd><code>{data.report.recipe_id}</code></dd></div><div><dt>Before patch</dt><dd>Exit {data.report.before_exit}</dd></div><div><dt>After patch</dt><dd>Exit {data.report.after_exit}</dd></div><div><dt>Evidence references</dt><dd>{data.report.evidence_ids?.join(', ') || '—'}</dd></div></dl></>}
    </section>}
    {data.patch && <section className="repair-section"><h3>Reviewed diff</h3><p className="muted">Patch SHA-256: <code>{data.report?.patch_sha256}</code></p><pre className="repair-diff">{data.patch}</pre>
      {c.State === 'patch_ready' && canAct && <button className="primary" disabled={mutation.isPending} onClick={() => mutation.mutate({ action: 'review', body: { expected_version: c.Version } })}>Request publication review</button>}
      {c.State === 'awaiting_publish_approval' && canAct && data.review_digest && <div className="repair-decision"><p>Confirm the diff, test result, repository, and base revision above. Approval expires after 15 minutes and binds this exact patch digest.</p><button className="danger" disabled={mutation.isPending} onClick={() => mutation.mutate({ action: 'publish', body: { expected_version: c.Version, review_digest: data.review_digest } })}>Approve draft PR publication</button></div>}
    </section>}
    {data.publish_approval && <p className="repair-note">Publication approved by {data.publish_approval.ActorID}. {approvalExpired ? 'Approval expired; only reconciliation of existing remote effects is allowed.' : `Expires ${new Date(data.publish_approval.ExpiresAt).toLocaleString()}.`}</p>}
    {data.publication && <section className="repair-section"><h3>Pull request</h3><dl className="repair-facts"><div><dt>Status</dt><dd>{label(data.publication.State)}</dd></div><div><dt>Branch</dt><dd><code>{data.publication.BranchName}</code></dd></div><div><dt>Head</dt><dd><code>{shortSHA(data.publication.HeadSHA)}</code></dd></div></dl>{data.publication.PRURL ? <a href={data.publication.PRURL} target="_blank" rel="noreferrer">Open draft PR #{data.publication.PRNumber} ↗</a> : <p className="muted">The publisher has not confirmed a PR yet.</p>}{data.publication.LastError && <p className="repair-note">{data.publication.LastError}</p>}</section>}
    {data.deployments.length > 0 && <section className="repair-section"><h3>Deployment and recovery</h3>{data.deployments.map((deployment) => <dl className="repair-facts" key={deployment.DeploymentID}><div><dt>Deployment</dt><dd><code>{deployment.DeploymentID}</code></dd></div><div><dt>Revision</dt><dd><code>{shortSHA(deployment.RevisionSHA)}</code></dd></div><div><dt>Phase</dt><dd>{label(deployment.Phase)}</dd></div><div><dt>Verification</dt><dd>{label(deployment.VerificationStatus)}</dd></div></dl>)}</section>}
    {c.State === 'merged' && <p className="repair-note">The PR is merged. The deployment pipeline must send a signed start event before rollout to capture a baseline, then a completion event for the same revision.</p>}
    {c.State === 'verifying' && <p className="repair-note">Waiting for three spaced observations of healthy processing, falling backlog, and the deployed merge revision.</p>}
    {c.State === 'recovered' && <p className="repair-success">Recovery verified from the deployed revision and three workload observations.</p>}
    {c.State === 'inconclusive' && <p className="repair-note">Recovery could not be proven. Inspect the deployment and telemetry before closing the incident.</p>}
    <section className="repair-section"><h3>Activity</h3><ol className="repair-activity">{data.events.map((event) => <li key={event.ID}><details><summary><time>{new Date(event.CreatedAt).toLocaleString()}</time><strong>{label(event.Type)}</strong><span>{event.ActorID}</span></summary><pre>{event.DetailsJSON}</pre></details></li>)}</ol></section>
  </div>
}

function RepairError({ error, retry }: { error: unknown; retry: () => void }) {
  const conflict = error instanceof APIError && error.status === 409
  return <div className="repair-error" role="alert"><strong>{conflict ? 'Case changed' : 'Request failed'}</strong><span>{error instanceof Error ? error.message : 'Unable to load code repair data.'}</span><button className="quiet-button" onClick={retry}>{conflict ? 'Refresh case' : 'Retry'}</button></div>
}
