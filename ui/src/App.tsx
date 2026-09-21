import { FormEvent, ReactNode, useState } from 'react'
import * as Tabs from '@radix-ui/react-tabs'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, NavLink, Navigate, Route, Routes, useLocation, useParams, useSearchParams } from 'react-router-dom'
import { Action, api, APIError, Detail, Incident } from './api'

type User = { id: string; username: string; role: string }
type ReasoningProfile = { provider: string; base_url: string; model: string; credential_ref: string; credential_available: boolean; credential_source?: string; json_mode: boolean; restart_required?: boolean }
type GrafanaProfile = { base_url: string; credential_ref: string; credential_available: boolean; metrics_source_uid: string; logs_source_uid: string; error_rate_query: string; latency_query: string; queue_query: string; replica_query: string; logs_query: string; deploy_logs_query: string; restart_required?: boolean }
type ConnectionStatus = { reasoning: Record<string, unknown>; grafana: Record<string, unknown>; prometheus: Record<string, unknown>; alertmanager: Record<string, unknown>; loki: Record<string, unknown> }
type PlaygroundData = { enabled: boolean; modes: string[]; state?: Record<string, unknown> }
type IconName = 'incident' | 'approval' | 'connection' | 'playground' | 'settings' | 'search' | 'arrow' | 'clock' | 'target' | 'shield' | 'activity' | 'logout'

export function App() {
  const session = useQuery({ queryKey: ['session'], queryFn: () => api<{ user: User }>('/api/v1/session'), retry: false })
  if (session.isLoading) return <Centered><Spinner /> Checking session…</Centered>
  if (session.isError || !session.data) return <Login onSuccess={() => session.refetch()} />
  return <Shell user={session.data.user} />
}

function Login({ onSuccess }: { onSuccess: () => void }) {
  const [error, setError] = useState('')
  const mutation = useMutation({ mutationFn: (data: { Username: string; Password: string }) => api('/api/v1/session', { method: 'POST', body: JSON.stringify(data) }), onSuccess, onError: (e) => setError(e.message) })
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const data = new FormData(event.currentTarget)
    mutation.mutate({ Username: String(data.get('username')), Password: String(data.get('password')) })
  }
  return <main className="login-page"><form className="login-panel" onSubmit={submit}>
    <Brand /><div><p className="eyebrow">Operator console</p><h1>Sign in to Triovexa</h1><p className="muted">Use an account created by a Triovexa administrator.</p></div>
    <label>Username<input name="username" autoComplete="username" required autoFocus /></label>
    <label>Password<input name="password" type="password" autoComplete="current-password" required /></label>
    {error && <ErrorBox>{error}</ErrorBox>}
    <button className="primary" disabled={mutation.isPending}>{mutation.isPending ? 'Signing in…' : 'Sign in'}</button>
  </form></main>
}

function Shell({ user }: { user: User }) {
  const location = useLocation()
  const logout = useMutation({ mutationFn: () => api('/api/v1/session/logout', { method: 'DELETE' }), onSuccess: () => window.location.reload() })
  const incidentDetail = /^\/ui\/incidents\/[^/]+$/.test(location.pathname)
  return <div className="shell"><a className="skip-link" href="#main-content">Skip to main content</a>
    <aside className="primary-nav"><Brand /><nav aria-label="Primary navigation">
      <NavItem to="/ui/incidents" icon="incident">Incidents</NavItem><NavItem to="/ui/approvals" icon="approval">Approvals</NavItem><NavItem to="/ui/connections" icon="connection">Connections</NavItem><NavItem to="/ui/playground" icon="playground">Playground</NavItem><NavItem to="/ui/settings" icon="settings">Settings</NavItem>
    </nav><div className="account"><span className="avatar">{user.username.slice(0, 1).toUpperCase()}</span><div><strong>{user.username}</strong><small>{user.role}</small></div><button className="icon-button" aria-label="Sign out" title="Sign out" onClick={() => logout.mutate()}><Icon name="logout" /></button></div></aside>
    <main id="main-content" tabIndex={-1} className={`content${incidentDetail ? ' incident-route' : ''}`}><Routes>
      <Route path="/ui/incidents" element={<Incidents />} /><Route path="/ui/incidents/:id" element={<IncidentDetail />} /><Route path="/ui/approvals" element={<Approvals />} /><Route path="/ui/connections" element={<Connections />} /><Route path="/ui/playground" element={<Playground />} /><Route path="/ui/settings" element={<Settings />} /><Route path="*" element={<Navigate to="/ui/incidents" replace />} />
    </Routes></main>
  </div>
}

function Brand() { return <Link className="brand" to="/ui/incidents" aria-label="Triovexa home"><span className="brand-mark" aria-hidden><i /><i /><i /></span><span>Triovexa</span></Link> }
function NavItem({ to, icon, children }: { to: string; icon: IconName; children: ReactNode }) { return <NavLink to={to}><Icon name={icon} /><span>{children}</span></NavLink> }

function Incidents() {
  const [params, setParams] = useSearchParams()
  const query = params.toString()
  const incidents = useQuery({ queryKey: ['incidents', query], queryFn: () => api<{ items: Incident[]; total: number; page: number; page_size: number }>(`/api/v1/incidents?${query}`) })
  const update = (key: string, value: string) => { const next = new URLSearchParams(params); value ? next.set(key, value) : next.delete(key); next.delete('page'); setParams(next) }
  const payload = incidents.data
  const activeFilters = ['status', 'severity'].filter((key) => params.get(key)).length
  const page = payload?.page ?? Number(params.get('page') || 1), pageSize = payload?.page_size ?? 25, pages = Math.max(1, Math.ceil((payload?.total ?? 0) / pageSize))
  const setPage = (value: number) => { const next = new URLSearchParams(params); value > 1 ? next.set('page', String(value)) : next.delete('page'); setParams(next) }
  return <div className="incidents-view">
    <PageHeader title="Incidents" subtitle="Operational events that require investigation or recovery." end={<span className="result-count">{payload?.total ?? '—'} total</span>} />
    {incidents.isRefetchError && payload && <SystemState kind="stale" title="Showing stale incident data" text="The latest refresh failed. Existing results remain visible." retry={() => incidents.refetch()} compact />}
    <div className="feed-toolbar"><label className="search-field"><Icon name="search" /><input aria-label="Search incidents" placeholder="Search incidents" value={params.get('q') ?? ''} onChange={(e) => update('q', e.target.value)} /></label>{activeFilters > 0 && <button className="quiet-button" onClick={() => { const next = new URLSearchParams(params); next.delete('status'); next.delete('severity'); setParams(next) }}>Clear filters</button>}</div>
    <div className="incident-feed-layout"><aside className="filter-rail" aria-label="Incident filters"><div className="filter-heading"><span>Filters</span>{activeFilters > 0 && <Count value={activeFilters} />}</div><FilterSelect label="Severity" value={params.get('severity') ?? ''} onChange={(value) => update('severity', value)} options={['critical', 'high', 'medium', 'low']} /><FilterSelect label="Status" value={params.get('status') ?? ''} onChange={(value) => update('status', value)} options={['awaiting_approval', 'executing_action', 'resolved', 'escalated']} /></aside>
      <section className="incident-feed" aria-label="Incident results" aria-busy={incidents.isFetching}><div className="feed-columns" aria-hidden><span>Incident</span><span>Target</span><span>Severity</span><span>Status</span><span>Updated</span></div>{incidents.isLoading ? <State title="Loading incidents…" /> : incidents.isError ? <RequestState error={incidents.error} retry={() => incidents.refetch()} /> : !payload || payload.items.length === 0 ? <Empty title="No matching incidents" text="Change the search or filters to see other operational events." /> : payload.items.map((item) => <IncidentRow key={item.ID} item={item} />)}{payload && payload.total > 0 && <nav className="pagination" aria-label="Incident pages"><button disabled={page <= 1 || incidents.isFetching} onClick={() => setPage(page - 1)}>Previous</button><span>Page <strong>{page}</strong> of {pages}</span><button disabled={page >= pages || incidents.isFetching} onClick={() => setPage(page + 1)}>Next</button></nav>}</section>
    </div>
  </div>
}

function FilterSelect({ label, value, onChange, options }: { label: string; value: string; onChange: (value: string) => void; options: string[] }) { return <label className="filter-control"><span>{label}</span><select aria-label={label} value={value} onChange={(e) => onChange(e.target.value)}><option value="">All</option>{options.map((option) => <option key={option} value={option}>{humanize(option)}</option>)}</select></label> }
function IncidentRow({ item }: { item: Incident }) { return <Link className="incident-row" to={`/ui/incidents/${item.ID}`}><span className={`severity-line severity-line-${item.Severity}`} aria-hidden /><span className="incident-identity"><strong>{item.Title}</strong><span><code>{item.ID.slice(0, 8)}</code> · {item.AlertSource || 'unknown source'}</span></span><span className="target-cell"><strong>{item.ServiceName || 'Unknown service'}</strong><small>{item.Environment || 'No environment'}</small></span><Status value={item.Severity} /><Status value={item.State} /><span className="time-cell"><RelativeTime value={item.UpdatedAt} /><Icon name="arrow" /></span></Link> }

function IncidentDetail() {
  const { id = '' } = useParams()
  const client = useQueryClient()
  const detail = useQuery({ queryKey: ['incident', id], queryFn: () => api<Detail>(`/api/v1/incidents/${id}`), refetchInterval: document.visibilityState === 'visible' ? 5000 : false })
  const actionMutation = useMutation({ mutationFn: ({ action, operation }: { action: Action; operation: string }) => api(`/api/v1/actions/${action.ID}/${operation}`, { method: 'POST', body: '{}' }), onSuccess: () => client.invalidateQueries({ queryKey: ['incident', id] }), retry: false })
  if (detail.isLoading) return <State title="Loading incident…" />
  if (!detail.data) return <RequestState error={detail.error} retry={() => detail.refetch()} />
  const data = detail.data
  const activeActions = data.candidate_actions.filter((action) => ['awaiting_approval', 'approved', 'executing'].includes(action.Status))
  return <div className="incident-workspace">
    <header className="incident-header"><Link className="back" to="/ui/incidents">← All incidents</Link><div className="incident-kicker"><Status value={data.incident.Severity} /><span>{data.incident.AlertSource || 'Alert'}</span><Status value={data.incident.State} /></div><h1>{data.incident.Title}</h1><div className="incident-meta"><span><Icon name="target" />{data.incident.ServiceName}</span><span>{data.incident.Environment}</span><span><Icon name="clock" /><RelativeTime value={data.incident.CreatedAt} /></span><code>{data.incident.ID}</code></div></header>
    {detail.isRefetchError && <SystemState kind="stale" title="Incident data may be stale" text="The background refresh failed. Verify the server connection before acting." retry={() => detail.refetch()} compact />}{actionMutation.isError && <RequestState error={actionMutation.error} retry={() => { actionMutation.reset(); detail.refetch() }} />}
    <div className="incident-body"><Tabs.Root className="investigation" defaultValue="overview"><Tabs.List aria-label="Incident information"><Tabs.Trigger value="overview">Overview</Tabs.Trigger><Tabs.Trigger value="evidence">Evidence <Count value={data.evidence.length} /></Tabs.Trigger><Tabs.Trigger value="actions">Actions <Count value={data.candidate_actions.length} /></Tabs.Trigger><Tabs.Trigger value="activity">Activity <Count value={data.audit_events.length} /></Tabs.Trigger></Tabs.List>
      <Tabs.Content value="overview"><TriageSummary triage={data.triage} /><SectionHeading title="Recovery status" meta={data.verification_results.length ? `${data.verification_results.length} checks` : 'Waiting for execution'} /><VerificationList items={data.verification_results} /></Tabs.Content>
      <Tabs.Content value="evidence"><EvidenceList items={data.evidence} /></Tabs.Content>
      <Tabs.Content value="actions"><ActionList actions={data.candidate_actions} mutate={(action, operation) => actionMutation.mutate({ action, operation })} busy={actionMutation.isPending} /></Tabs.Content>
      <Tabs.Content value="activity"><Timeline items={data.audit_events} /></Tabs.Content>
    </Tabs.Root><aside className="incident-rail"><section className="rail-section action-rail"><div className="rail-title"><div><p className="eyebrow">Decision required</p><h2>Operator actions</h2></div><Icon name="shield" /></div><p className="muted">Confirm the target and evidence before changing workload state.</p><ActionList actions={activeActions} mutate={(action, operation) => actionMutation.mutate({ action, operation })} busy={actionMutation.isPending} compact /></section><section className="rail-section"><div className="rail-title"><div><p className="eyebrow">Latest events</p><h2>Activity</h2></div><Icon name="activity" /></div><Timeline items={data.audit_events.slice(-5)} compact /></section></aside></div>
  </div>
}

function TriageSummary({ triage }: { triage: Record<string, unknown> | null }) {
  if (!triage) return <Empty title="Investigation in progress" text="Triage has not produced a reliable summary yet." />
  const hypotheses = stringArray(triage.Hypotheses), nextSteps = stringArray(triage.NextSteps)
  return <article className="triage-summary"><SectionHeading title="Current summary" meta={String(triage.ConfidenceNotes || 'Generated from collected evidence')} /><p className="lead">{String(triage.Summary || 'No summary was returned.')}</p>{Boolean(triage.BlastRadius) && <div className="callout"><span>Impact</span><p>{String(triage.BlastRadius)}</p></div>}{hypotheses.length > 0 && <SummaryList title="Likely causes" items={hypotheses} />}{nextSteps.length > 0 && <SummaryList title="Suggested next steps" items={nextSteps} ordered />}</article>
}
function SummaryList({ title, items, ordered = false }: { title: string; items: string[]; ordered?: boolean }) { const List = ordered ? 'ol' : 'ul'; return <section className="summary-block"><h3>{title}</h3><List>{items.map((item, index) => <li key={`${item}-${index}`}>{item}</li>)}</List></section> }

function ActionList({ actions, mutate, busy, compact = false }: { actions: Action[]; mutate: (action: Action, operation: string) => void; busy: boolean; compact?: boolean }) {
  if (!actions.length) return <Empty title="No action available" text="There is no operator decision waiting for this incident." compact />
  return <div className={`action-list${compact ? ' compact' : ''}`}>{actions.map((action) => <article className="action-item" key={action.ID}><div className="action-title"><div><span className="action-type">{humanize(action.ActionType)}</span><code>{action.TargetResource}</code></div><Status value={action.Status} /></div><p>{action.Rationale}</p><dl><div><dt>Risk</dt><dd>{humanize(action.RiskLevel)}</dd></div>{!compact && <><div><dt>Target</dt><dd><code>{action.TargetResource}</code></dd></div><div><dt>Parameters</dt><dd><InlineData value={safeJSON(action.ParametersJSON)} /></dd></div></>}</dl><div className="button-row">{action.Status === 'awaiting_approval' && <><button className="primary" disabled={busy} onClick={() => mutate(action, 'approve')}>Approve {verb(action.ActionType)}</button><button className="quiet-button" disabled={busy} onClick={() => mutate(action, 'reject')}>Reject</button></>}{action.Status === 'approved' && <button className="danger" disabled={busy} onClick={() => mutate(action, 'execute')}>Execute {verb(action.ActionType)}</button>}</div></article>)}</div>
}

function Approvals() {
  const result = useQuery({ queryKey: ['approvals'], queryFn: () => api<{ items: { incident: Incident; action: Action }[] }>('/api/v1/approvals') }), items = result.data?.items ?? []
  return <div className="standard-page"><PageHeader title="Approvals" subtitle="Actions waiting for an accountable operator decision." end={<span className="result-count">{items.length} waiting</span>} />{result.isLoading ? <State title="Loading approvals…" /> : result.isError ? <ErrorBox>{result.error.message}</ErrorBox> : !items.length ? <Empty title="Approval queue is clear" text="New remediation proposals will appear here when operator review is required." /> : <div className="approval-list">{items.map(({ incident, action }) => <Link className="approval-row" key={action.ID} to={`/ui/incidents/${incident.ID}`}><span className={`severity-line severity-line-${incident.Severity}`} /><div><span className="eyebrow">{humanize(action.ActionType)}</span><strong>{incident.Title}</strong><small>{action.TargetResource}</small></div><Status value={action.RiskLevel} /><RelativeTime value={action.CreatedAt} /><Icon name="arrow" /></Link>)}</div>}</div>
}

function Connections() {
  const result = useQuery({ queryKey: ['connections'], queryFn: () => api<ConnectionStatus>('/api/v1/connections') })
  const reasoning = useQuery({ queryKey: ['connections', 'reasoning', 'config'], queryFn: () => api<ReasoningProfile>('/api/v1/connections/reasoning/config') })
  const grafana = useQuery({ queryKey: ['connections', 'grafana', 'config'], queryFn: () => api<GrafanaProfile>('/api/v1/connections/grafana/config') })
  return <div className="standard-page"><PageHeader title="Connections" subtitle="Configure telemetry and AI integrations used by the incident workflow." />{result.isLoading ? <State title="Checking connections…" /> : result.isError ? <RequestState error={result.error} retry={() => result.refetch()} /> : result.data ? <div className="settings-list">
    <ConnectionRow name="Reasoning provider" description="Structured triage and remediation proposals" data={result.data.reasoning} testPath="reasoning" />
    <ConnectionRow name="Grafana" description="Metrics and logs used as incident evidence" data={result.data.grafana} />
    <ConnectionRow name="Prometheus" description="Workload metrics, recording rules, and alert evaluation" data={result.data.prometheus} testPath="prometheus" />
    <ConnectionRow name="Alertmanager" description="Alert routing and delivery status for the playground stack" data={result.data.alertmanager} testPath="alertmanager" />
    <ConnectionRow name="Loki" description="Centralized logs collected from Triovexa and the workload stack" data={result.data.loki} testPath="loki" />
  </div> : <Empty title="Connections unavailable" text="The server did not return integration status." />}<section className="configuration-section" aria-labelledby="connection-configuration"><SectionHeading title="Connection configuration" meta="AI keys entered here remain in server memory until restart" /><h2 id="connection-configuration" className="sr-only">Connection configuration</h2>{reasoning.isLoading || grafana.isLoading ? <State title="Loading connection configuration…" /> : reasoning.isError ? <RequestState error={reasoning.error} retry={() => reasoning.refetch()} /> : grafana.isError ? <RequestState error={grafana.error} retry={() => grafana.refetch()} /> : <div className="configuration-grid"><ReasoningConfiguration initial={reasoning.data!} /><GrafanaConfiguration initial={grafana.data!} /></div>}</section></div>
}

function ReasoningConfiguration({ initial }: { initial: ReasoningProfile }) {
  const client = useQueryClient(), [form, setForm] = useState(initial), [apiKey, setAPIKey] = useState('')
  const payload = () => ({ provider: 'openai-compatible', base_url: form.base_url, model: form.model, credential_ref: form.credential_ref || 'LLM_API_KEY', json_mode: form.json_mode, api_key: apiKey })
  const save = useMutation({ mutationFn: () => api<ReasoningProfile>('/api/v1/connections/reasoning/config', { method: 'PUT', body: JSON.stringify(payload()) }), onSuccess: (data) => { setForm(data); setAPIKey(''); client.invalidateQueries({ queryKey: ['connections'] }); client.invalidateQueries({ queryKey: ['settings'] }) } })
  const test = useMutation({ mutationFn: () => api<Record<string, unknown>>('/api/v1/connections/reasoning/test', { method: 'POST', body: JSON.stringify(payload()) }) })
  return <form className="connection-editor" onSubmit={(event) => { event.preventDefault(); save.mutate() }}><header><div><p className="eyebrow">Reasoning</p><h3>OpenAI-compatible API</h3></div><Status value={form.credential_available ? 'credential_ready' : 'credential_missing'} /></header><Field label="API base URL" hint="API root such as https://api.openai.com or another Chat Completions-compatible endpoint."><input type="url" value={form.base_url} onChange={(e) => setForm({ ...form, provider: 'openai-compatible', base_url: e.target.value })} required /></Field><Field label="Model"><input value={form.model} onChange={(e) => setForm({ ...form, provider: 'openai-compatible', model: e.target.value })} required /></Field><Field label="API key" hint={form.credential_available ? 'Leave blank to keep the active in-memory key. Enter a new value to replace it.' : 'Stored only in server memory and cleared when Triovexa restarts.'}><input type="password" value={apiKey} onChange={(e) => setAPIKey(e.target.value)} autoComplete="new-password" spellCheck={false} required={!form.credential_available} /></Field><label className="check-field"><input type="checkbox" checked={form.json_mode} onChange={(e) => setForm({ ...form, provider: 'openai-compatible', json_mode: e.target.checked })} /> Request provider JSON response mode</label><div className="button-row"><button className="primary" type="submit" disabled={save.isPending}>{save.isPending ? 'Activating…' : 'Save & activate'}</button><button type="button" disabled={test.isPending || (!apiKey && !form.credential_available)} onClick={() => test.mutate()}>{test.isPending ? 'Testing…' : 'Test connection'}</button></div><MutationFeedback mutation={save} success="Provider activated. Reasoning mode is now using the configured LLM." /><MutationFeedback mutation={test} success="Provider returned valid structured JSON." /></form>
}

function GrafanaConfiguration({ initial }: { initial: GrafanaProfile }) {
  const client = useQueryClient(), [form, setForm] = useState(initial)
  const payload = () => ({ base_url: form.base_url, credential_ref: form.credential_ref, metrics_source_uid: form.metrics_source_uid, logs_source_uid: form.logs_source_uid, error_rate_query: form.error_rate_query, latency_query: form.latency_query, queue_query: form.queue_query, replica_query: form.replica_query, logs_query: form.logs_query, deploy_logs_query: form.deploy_logs_query })
  const save = useMutation({ mutationFn: () => api<GrafanaProfile>('/api/v1/connections/grafana/config', { method: 'PUT', body: JSON.stringify(payload()) }), onSuccess: (data) => { setForm(data); client.invalidateQueries({ queryKey: ['connections'] }) } })
  const test = useMutation({ mutationFn: () => api<Record<string, unknown>>('/api/v1/connections/grafana/test', { method: 'POST', body: JSON.stringify(payload()) }) })
  const preview = useMutation({ mutationFn: () => api<{ evidence: Record<string, unknown>[] }>('/api/v1/connections/grafana/preview', { method: 'POST', body: JSON.stringify(payload()) }) })
  const set = (key: keyof GrafanaProfile, value: string) => setForm({ ...form, [key]: value })
  return <form className="connection-editor" onSubmit={(event) => { event.preventDefault(); save.mutate() }}><header><div><p className="eyebrow">Observability</p><h3>Grafana onboarding</h3></div><Status value={form.credential_available ? 'credential_ready' : 'credential_missing'} /></header><Field label="Grafana URL"><input type="url" value={form.base_url} onChange={(e) => set('base_url', e.target.value)} required /></Field><Field label="Credential reference" hint="Environment variable containing a Grafana service-account token."><input value={form.credential_ref} onChange={(e) => set('credential_ref', e.target.value.toUpperCase())} pattern="[A-Z][A-Z0-9_]{1,127}" required /></Field><div className="field-pair"><Field label="Prometheus datasource UID"><input value={form.metrics_source_uid} onChange={(e) => set('metrics_source_uid', e.target.value)} required /></Field><Field label="Loki datasource UID"><input value={form.logs_source_uid} onChange={(e) => set('logs_source_uid', e.target.value)} required /></Field></div><details className="advanced-fields"><summary>Query templates</summary><Field label="Error rate query"><textarea value={form.error_rate_query} onChange={(e) => set('error_rate_query', e.target.value)} /></Field><Field label="Latency query"><textarea value={form.latency_query} onChange={(e) => set('latency_query', e.target.value)} /></Field><Field label="Queue query"><textarea value={form.queue_query} onChange={(e) => set('queue_query', e.target.value)} /></Field><Field label="Replica query"><textarea value={form.replica_query} onChange={(e) => set('replica_query', e.target.value)} /></Field><Field label="Logs query"><textarea value={form.logs_query} onChange={(e) => set('logs_query', e.target.value)} /></Field><Field label="Deploy logs query"><textarea value={form.deploy_logs_query} onChange={(e) => set('deploy_logs_query', e.target.value)} /></Field></details><div className="button-row"><button className="primary" type="submit" disabled={save.isPending}>{save.isPending ? 'Saving…' : 'Save configuration'}</button><button type="button" disabled={test.isPending} onClick={() => test.mutate()}>{test.isPending ? 'Testing…' : 'Test connection'}</button><button type="button" disabled={preview.isPending} onClick={() => preview.mutate()}>{preview.isPending ? 'Querying…' : 'Preview evidence'}</button></div><MutationFeedback mutation={save} success="Saved. Restart Triovexa to activate this Grafana profile." /><MutationFeedback mutation={test} success="Grafana connection and datasource discovery succeeded." /><MutationFeedback mutation={preview} success={`Evidence preview returned ${preview.data?.evidence?.length ?? 0} records.`} /></form>
}

function Playground() {
  const client = useQueryClient()
  const result = useQuery({ queryKey: ['playground'], queryFn: () => api<PlaygroundData>('/api/v1/playground'), refetchInterval: document.visibilityState === 'visible' ? 3000 : false, retry: 1 })
  const fault = useMutation({ mutationFn: (mode: string) => api('/api/v1/playground/faults', { method: 'POST', body: JSON.stringify({ mode }) }), onSuccess: () => client.invalidateQueries({ queryKey: ['playground'] }), retry: false })
  const state = result.data?.state ?? {}
  return <div className="standard-page"><PageHeader title="Playground" subtitle="Inject bounded workload faults and watch the real alert, approval, execution, and recovery flow." />{result.isRefetchError && result.data && <SystemState kind="stale" title="Supervisor data is stale" text="The last refresh failed; controls are disabled until connectivity returns." retry={() => result.refetch()} compact />}{result.isLoading ? <State title="Connecting to workload supervisor…" /> : result.isError && !result.data ? <RequestState error={result.error} retry={() => result.refetch()} /> : !result.data?.enabled ? <SystemState kind="disconnected" title="Playground is unavailable" text="Configure WORKLOAD_CONTROL_BASE_URL to enable bounded fault controls." retry={() => result.refetch()} /> : <><section className="workload-state" aria-live="polite"><SectionHeading title="Queue worker" meta={result.isFetching ? 'Refreshing…' : 'Live supervisor state'} /><dl>{Object.entries(state).map(([key, value]) => <div key={key}><dt>{humanize(key)}</dt><dd>{typeof value === 'boolean' ? <Status value={value ? 'yes' : 'no'} /> : String(value)}</dd></div>)}</dl></section><section className="fault-controls" aria-labelledby="fault-controls-heading"><div><p className="eyebrow">Bounded controls</p><h2 id="fault-controls-heading">Fault injection</h2><p>Stall keeps producing jobs while the worker stops consuming. Restart remediation clears the fault and starts a new worker generation.</p></div><div className="fault-actions"><button className="danger" disabled={fault.isPending || result.isRefetchError} onClick={() => fault.mutate('stall')}>Inject worker stall</button><button disabled={fault.isPending || result.isRefetchError} onClick={() => fault.mutate('fail')}>Inject processing failures</button><button className="primary" disabled={fault.isPending || result.isRefetchError} onClick={() => fault.mutate('healthy')}>Restore healthy mode</button></div><MutationFeedback mutation={fault} success="Fault mode updated. Prometheus and Alertmanager will evaluate the resulting telemetry." /></section></>}</div>
}

function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) { return <label className="form-field"><span>{label}</span>{children}{hint && <small>{hint}</small>}</label> }
function MutationFeedback({ mutation, success }: { mutation: { isError: boolean; error: Error | null; isSuccess: boolean }; success: string }) { return <div className="mutation-feedback" aria-live="polite">{mutation.isError ? <RequestState error={mutation.error} /> : mutation.isSuccess ? <p className="success-message">{success}</p> : null}</div> }

function Settings() {
  const client = useQueryClient(), result = useQuery({ queryKey: ['settings'], queryFn: () => api<Record<string, unknown>>('/api/v1/settings') })
  const update = useMutation({ mutationFn: (payload: Record<string, unknown>) => api('/api/v1/settings', { method: 'PATCH', body: JSON.stringify(payload) }), onSuccess: () => client.invalidateQueries({ queryKey: ['settings'] }) })
  const enabled = Boolean(result.data?.kill_switch_enabled)
  const runtime = (result.data?.runtime ?? {}) as Record<string, unknown>
  return <div className="standard-page"><PageHeader title="Settings" subtitle="Deployment posture, active adapters, and execution safety controls." />{result.isLoading ? <State title="Loading settings…" /> : result.isError ? <RequestState error={result.error} retry={() => result.refetch()} /> : <div className="settings-list"><SettingRow title="Deployment mode" description="Controls authentication and external endpoint policy"><span className="setting-value">{humanize(String(result.data?.deployment_mode || 'unknown'))}</span></SettingRow><SettingRow title="Environment" description="Default environment attached to operational events"><span className="setting-value">{String(result.data?.environment || 'unknown')}</span></SettingRow><SettingRow title="Reasoning mode" description="Choose deterministic heuristics or the configured LLM provider."><select aria-label="Reasoning mode" value={String(runtime.reasoning || 'heuristic')} disabled={update.isPending} onChange={(e) => update.mutate({ reasoning_mode: e.target.value })}><option value="heuristic">Heuristic</option><option value="llm">LLM provider</option></select></SettingRow><SettingRow title="Observability mode" description="Choose local workload telemetry or Grafana datasource queries."><select aria-label="Observability mode" value={String(runtime.observability || 'demo')} disabled={update.isPending} onChange={(e) => update.mutate({ observability_mode: e.target.value })}><option value="demo">Local workload</option><option value="grafana">Grafana</option></select></SettingRow><SettingRow title="Execution kill switch" description="Persistently blocks new approvals and executions."><div className="setting-action"><Status value={enabled ? 'enabled' : 'disabled'} /><button className={enabled ? '' : 'danger'} disabled={update.isPending} onClick={() => update.mutate({ enabled: !enabled })}>{enabled ? 'Disable' : 'Enable'}</button></div></SettingRow></div>}{update.isError && <RequestState error={update.error} retry={() => update.reset()} />}</div>
}

function ConnectionRow({ name, description, data, testPath }: { name: string; description: string; data: Record<string, unknown>; testPath?: string }) {
  const test = useMutation({ mutationFn: () => api<Record<string, unknown>>(`/api/v1/connections/${testPath}/test`, { method: 'POST', body: '{}' }) })
  const details = Object.entries(data).filter(([key]) => key !== 'configured')
  return <section className="setting-row"><div><h2>{name}</h2><p>{description}</p></div><div className="connection-meta">{details.map(([key, value]) => value ? <span key={key}><small>{humanize(key)}</small><code>{String(value)}</code></span> : null)}</div><div className="setting-action"><Status value={data.configured ? 'connected' : 'not_configured'} />{testPath && <button onClick={() => test.mutate()} disabled={!data.configured || test.isPending}>{test.isPending ? 'Testing…' : 'Test connection'}</button>}</div>{test.isError && <p className="connection-check connection-check-error">{test.error.message}</p>}{test.data && <p className="connection-check"><Status value={test.data.status} /><span>{String(test.data.latency_ms || '—')} ms</span></p>}</section>
}
function SettingRow({ title, description, children }: { title: string; description: string; children: ReactNode }) { return <section className="setting-row"><div><h2>{title}</h2><p>{description}</p></div><div className="setting-control">{children}</div></section> }

function EvidenceList({ items }: { items: Record<string, unknown>[] }) {
  if (!items.length) return <Empty title="No evidence collected" text="Evidence will appear after telemetry collection completes." />
  return <div className="evidence-list"><SectionHeading title="Collected evidence" meta={`${items.length} records`} />{items.map((item, index) => <details className="evidence-row" key={String(item.ID ?? index)} open={index === 0}><summary><span className="evidence-kind">{String(item.Type ?? 'Evidence')}</span><span className="evidence-copy"><strong>{String(item.Snippet ?? 'Evidence record')}</strong><small>{String(item.Source ?? 'Unknown source')} · <RelativeTime value={String(item.Timestamp ?? '')} /></small></span><span className="disclosure">+</span></summary><div className="evidence-detail"><InlineData value={safeJSON(item.MetadataJSON)} /></div></details>)}</div>
}
function VerificationList({ items }: { items: Record<string, unknown>[] }) { return items.length ? <div className="verification-list">{items.map((item, index) => <div className="verification-row" key={String(item.ID ?? index)}><Status value={String(item.Status)} /><div><strong>{String(item.Notes || 'Verification completed')}</strong><InlineData value={safeJSON(item.EvidenceJSON)} /></div><RelativeTime value={String(item.CreatedAt ?? '')} /></div>)}</div> : <Empty title="No verification yet" text="Recovery is confirmed only after three healthy observations." compact /> }
function Timeline({ items, compact = false }: { items: Record<string, unknown>[]; compact?: boolean }) { return items.length ? <ol className={`timeline${compact ? ' compact' : ''}`}>{[...items].reverse().map((item, index) => <li key={String(item.ID ?? index)}><span className={`timeline-dot timeline-dot-${String(item.Status || '').toLowerCase()}`} /><div><div className="timeline-heading"><strong>{humanize(String(item.StepName ?? 'event'))}</strong>{!compact && <Status value={String(item.Status ?? '')} />}</div><RelativeTime value={String(item.StartedAt ?? '')} />{!compact && item.DetailsJSON ? <details><summary>View event data</summary><InlineData value={safeJSON(item.DetailsJSON)} /></details> : null}</div></li>)}</ol> : <Empty title="No activity yet" text="Workflow events will appear here." compact /> }

function InlineData({ value }: { value: unknown }) {
  if (value === null || value === undefined || value === '' || (typeof value === 'object' && Object.keys(value as object).length === 0)) return <span className="muted">No additional data</span>
  if (typeof value !== 'object') return <code className="inline-data">{String(value)}</code>
  return <dl className="data-list">{Object.entries(value as Record<string, unknown>).map(([key, item]) => <div key={key}><dt>{humanize(key)}</dt><dd>{typeof item === 'object' ? JSON.stringify(item) : String(item)}</dd></div>)}</dl>
}

function SectionHeading({ title, meta }: { title: string; meta?: string }) { return <header className="section-heading"><h2>{title}</h2>{meta && <span>{meta}</span>}</header> }
function PageHeader({ title, subtitle, end }: { title: string; subtitle: string; end?: ReactNode }) { return <header className="page-header"><div><p className="eyebrow">Triovexa / Operations</p><h1>{title}</h1><p>{subtitle}</p></div>{end}</header> }
function Status({ value }: { value: unknown }) { const text = String(value || 'unknown'); return <span className={`status status-${text.replaceAll('_', '-')}`}><i />{humanize(text)}</span> }
function RelativeTime({ value }: { value: string }) { const date = new Date(value); if (Number.isNaN(date.valueOf())) return <time>Unknown time</time>; const minutes = Math.round((Date.now() - date.valueOf()) / 60000); const label = minutes < 1 ? 'just now' : minutes < 60 ? `${minutes}m ago` : minutes < 1440 ? `${Math.round(minutes / 60)}h ago` : `${Math.round(minutes / 1440)}d ago`; return <time dateTime={value} title={date.toLocaleString()}>{label}</time> }
function Count({ value }: { value: number }) { return <span className="count">{value}</span> }
function Empty({ title, text, compact = false }: { title: string; text: string; compact?: boolean }) { return <div className={`empty${compact ? ' compact' : ''}`}><strong>{title}</strong><p>{text}</p></div> }
function State({ title }: { title: string }) { return <div className="state"><Spinner />{title}</div> }
function Spinner() { return <span className="spinner" aria-hidden /> }
function Centered({ children }: { children: ReactNode }) { return <main className="centered">{children}</main> }
function ErrorBox({ children }: { children: ReactNode }) { return <div className="error" role="alert">{children}</div> }
function RequestState({ error, retry }: { error: unknown; retry?: () => void }) {
  const apiError = error instanceof APIError ? error : null
  if (apiError?.status === 403) return <SystemState kind="forbidden" title="Access denied" text="Your account does not have permission to perform this operation." retry={retry} />
  if (apiError?.status === 409) return <SystemState kind="conflict" title="State changed on the server" text="Another operator or workflow updated this record. Refresh before deciding again." retry={retry} />
  if (!apiError && error) return <SystemState kind="disconnected" title="Connection lost" text="Triovexa could not be reached. Check the server and retry." retry={retry} />
  return <SystemState kind="error" title="Request failed" text={error instanceof Error ? error.message : 'The requested data is unavailable.'} retry={retry} />
}
function SystemState({ kind, title, text, retry, compact = false }: { kind: string; title: string; text: string; retry?: () => void; compact?: boolean }) { return <section className={`system-state system-state-${kind}${compact ? ' compact' : ''}`} role={kind === 'error' || kind === 'forbidden' || kind === 'conflict' ? 'alert' : 'status'}><div><strong>{title}</strong><p>{text}</p></div>{retry && <button onClick={retry}>{kind === 'conflict' || kind === 'stale' ? 'Refresh' : 'Retry'}</button>}</section> }

function Icon({ name }: { name: IconName }) {
  const paths: Record<IconName, ReactNode> = {
    incident: <><path d="M12 3v5"/><path d="m9.5 6 2.5 2 2.5-2"/><rect x="4" y="11" width="16" height="9" rx="2"/><path d="M8 15h.01M12 15h4"/></>, approval: <><path d="M9 11l2 2 4-4"/><path d="M12 3 4.5 6v5c0 4.6 3.2 8.1 7.5 10 4.3-1.9 7.5-5.4 7.5-10V6L12 3Z"/></>, connection: <><path d="M8 12h8M12 8v8"/><path d="M7 5H5a2 2 0 0 0-2 2v2M17 5h2a2 2 0 0 1 2 2v2M7 19H5a2 2 0 0 1-2-2v-2M17 19h2a2 2 0 0 0 2-2v-2"/></>, playground: <><path d="M6 4h12v5a6 6 0 0 1-12 0V4Z"/><path d="M9 15v5M15 15v5M7 20h10"/></>, settings: <><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1-2.8 2.8-.1-.1a1.7 1.7 0 0 0-1.9-.3 1.7 1.7 0 0 0-1 1.6v.2h-4V21a1.7 1.7 0 0 0-1-1.6 1.7 1.7 0 0 0-1.9.3l-.1.1L4.2 17l.1-.1a1.7 1.7 0 0 0 .3-1.9A1.7 1.7 0 0 0 3 14H2.8v-4H3a1.7 1.7 0 0 0 1.6-1 1.7 1.7 0 0 0-.3-1.9L4.2 7 7 4.2l.1.1A1.7 1.7 0 0 0 9 4.6a1.7 1.7 0 0 0 1-1.6v-.2h4V3a1.7 1.7 0 0 0 1 1.6 1.7 1.7 0 0 0 1.9-.3l.1-.1L19.8 7l-.1.1a1.7 1.7 0 0 0-.3 1.9 1.7 1.7 0 0 0 1.6 1h.2v4H21a1.7 1.7 0 0 0-1.6 1Z"/></>, search: <><circle cx="11" cy="11" r="6"/><path d="m16 16 4 4"/></>, arrow: <path d="m9 18 6-6-6-6"/>, clock: <><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/></>, target: <><circle cx="12" cy="12" r="8"/><circle cx="12" cy="12" r="3"/></>, shield: <path d="M12 3 5 6v5c0 4.4 2.8 7.7 7 9.7 4.2-2 7-5.3 7-9.7V6l-7-3Z"/>, activity: <path d="M3 12h4l2-6 4 12 2-6h6"/>, logout: <><path d="M10 5H5v14h5M14 8l4 4-4 4M18 12H9"/></>,
  }
  return <svg className="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden>{paths[name]}</svg>
}

function humanize(value: string) { return value.replaceAll('_', ' ').replace(/\b\w/g, (letter) => letter.toUpperCase()) }
function verb(value: string) { return value.includes('restart') ? 'restart' : value.includes('pause') ? 'pause' : value.includes('resume') ? 'resume' : 'action' }
function safeJSON(value: unknown) { if (typeof value !== 'string') return value; try { return JSON.parse(value) } catch { return value } }
function stringArray(value: unknown) { return Array.isArray(value) ? value.map(String).filter(Boolean) : [] }
