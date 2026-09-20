import { FormEvent, ReactNode, useState } from 'react'
import * as Tabs from '@radix-ui/react-tabs'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, NavLink, Navigate, Route, Routes, useLocation, useParams, useSearchParams } from 'react-router-dom'
import { Action, api, Detail, Incident } from './api'

type User = { id: string; username: string; role: string }
type IconName = 'incident' | 'approval' | 'connection' | 'settings' | 'search' | 'arrow' | 'clock' | 'target' | 'shield' | 'activity' | 'logout'

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
  return <div className="shell">
    <aside className="primary-nav"><Brand /><nav aria-label="Primary navigation">
      <NavItem to="/ui/incidents" icon="incident">Incidents</NavItem><NavItem to="/ui/approvals" icon="approval">Approvals</NavItem><NavItem to="/ui/connections" icon="connection">Connections</NavItem><NavItem to="/ui/settings" icon="settings">Settings</NavItem>
    </nav><div className="account"><span className="avatar">{user.username.slice(0, 1).toUpperCase()}</span><div><strong>{user.username}</strong><small>{user.role}</small></div><button className="icon-button" aria-label="Sign out" title="Sign out" onClick={() => logout.mutate()}><Icon name="logout" /></button></div></aside>
    <main className={`content${incidentDetail ? ' incident-route' : ''}`}><Routes>
      <Route path="/ui/incidents" element={<Incidents />} /><Route path="/ui/incidents/:id" element={<IncidentDetail />} /><Route path="/ui/approvals" element={<Approvals />} /><Route path="/ui/connections" element={<Connections />} /><Route path="/ui/settings" element={<Settings />} /><Route path="*" element={<Navigate to="/ui/incidents" replace />} />
    </Routes></main>
  </div>
}

function Brand() { return <Link className="brand" to="/ui/incidents" aria-label="Triovexa home"><span className="brand-mark" aria-hidden><i /><i /><i /></span><span>Triovexa</span></Link> }
function NavItem({ to, icon, children }: { to: string; icon: IconName; children: ReactNode }) { return <NavLink to={to}><Icon name={icon} /><span>{children}</span></NavLink> }

function Incidents() {
  const [params, setParams] = useSearchParams()
  const query = params.toString()
  const incidents = useQuery({ queryKey: ['incidents', query], queryFn: () => api<{ items: Incident[]; total: number }>(`/api/v1/incidents?${query}`) })
  const update = (key: string, value: string) => { const next = new URLSearchParams(params); value ? next.set(key, value) : next.delete(key); next.delete('page'); setParams(next) }
  const payload = incidents.data
  const activeFilters = ['status', 'severity'].filter((key) => params.get(key)).length
  return <div className="incidents-view">
    <PageHeader title="Incidents" subtitle="Operational events that require investigation or recovery." end={<span className="result-count">{payload?.total ?? '—'} total</span>} />
    <div className="feed-toolbar"><label className="search-field"><Icon name="search" /><input aria-label="Search incidents" placeholder="Search incidents" value={params.get('q') ?? ''} onChange={(e) => update('q', e.target.value)} /></label>{activeFilters > 0 && <button className="quiet-button" onClick={() => { const next = new URLSearchParams(params); next.delete('status'); next.delete('severity'); setParams(next) }}>Clear filters</button>}</div>
    <div className="incident-feed-layout"><aside className="filter-rail" aria-label="Incident filters"><div className="filter-heading"><span>Filters</span>{activeFilters > 0 && <Count value={activeFilters} />}</div><FilterSelect label="Severity" value={params.get('severity') ?? ''} onChange={(value) => update('severity', value)} options={['critical', 'high', 'medium', 'low']} /><FilterSelect label="Status" value={params.get('status') ?? ''} onChange={(value) => update('status', value)} options={['awaiting_approval', 'executing_action', 'resolved', 'escalated']} /></aside>
      <section className="incident-feed" aria-label="Incident results"><div className="feed-columns" aria-hidden><span>Incident</span><span>Target</span><span>Severity</span><span>Status</span><span>Updated</span></div>{incidents.isLoading ? <State title="Loading incidents…" /> : incidents.isError ? <ErrorBox>{incidents.error.message}</ErrorBox> : !payload || payload.items.length === 0 ? <Empty title="No matching incidents" text="Change the search or filters to see other operational events." /> : payload.items.map((item) => <IncidentRow key={item.ID} item={item} />)}</section>
    </div>
  </div>
}

function FilterSelect({ label, value, onChange, options }: { label: string; value: string; onChange: (value: string) => void; options: string[] }) { return <label className="filter-control"><span>{label}</span><select aria-label={label} value={value} onChange={(e) => onChange(e.target.value)}><option value="">All</option>{options.map((option) => <option key={option} value={option}>{humanize(option)}</option>)}</select></label> }
function IncidentRow({ item }: { item: Incident }) { return <Link className="incident-row" to={`/ui/incidents/${item.ID}`}><span className={`severity-line severity-line-${item.Severity}`} aria-hidden /><span className="incident-identity"><strong>{item.Title}</strong><span><code>{item.ID.slice(0, 8)}</code> · {item.AlertSource || 'unknown source'}</span></span><span className="target-cell"><strong>{item.ServiceName || 'Unknown service'}</strong><small>{item.Environment || 'No environment'}</small></span><Status value={item.Severity} /><Status value={item.State} /><span className="time-cell"><RelativeTime value={item.UpdatedAt} /><Icon name="arrow" /></span></Link> }

function IncidentDetail() {
  const { id = '' } = useParams()
  const client = useQueryClient()
  const detail = useQuery({ queryKey: ['incident', id], queryFn: () => api<Detail>(`/api/v1/incidents/${id}`), refetchInterval: document.visibilityState === 'visible' ? 5000 : false })
  const actionMutation = useMutation({ mutationFn: ({ action, operation }: { action: Action; operation: string }) => api(`/api/v1/actions/${action.ID}/${operation}`, { method: 'POST', body: '{}' }), onSuccess: () => client.invalidateQueries({ queryKey: ['incident', id] }) })
  if (detail.isLoading) return <State title="Loading incident…" />
  if (detail.isError || !detail.data) return <ErrorBox>{detail.error?.message ?? 'Incident data is unavailable.'}</ErrorBox>
  const data = detail.data
  const activeActions = data.candidate_actions.filter((action) => ['awaiting_approval', 'approved', 'executing'].includes(action.Status))
  return <div className="incident-workspace">
    <header className="incident-header"><Link className="back" to="/ui/incidents">← All incidents</Link><div className="incident-kicker"><Status value={data.incident.Severity} /><span>{data.incident.AlertSource || 'Alert'}</span><Status value={data.incident.State} /></div><h1>{data.incident.Title}</h1><div className="incident-meta"><span><Icon name="target" />{data.incident.ServiceName}</span><span>{data.incident.Environment}</span><span><Icon name="clock" /><RelativeTime value={data.incident.CreatedAt} /></span><code>{data.incident.ID}</code></div></header>
    {actionMutation.isError && <ErrorBox>{actionMutation.error.message}</ErrorBox>}
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
  const result = useQuery({ queryKey: ['connections'], queryFn: () => api<{ reasoning: Record<string, unknown>; grafana: Record<string, unknown>; prometheus: Record<string, unknown>; alertmanager: Record<string, unknown> }>('/api/v1/connections') })
  return <div className="standard-page"><PageHeader title="Connections" subtitle="Server-managed integrations used for reasoning, alerting, and evidence." />{result.isLoading ? <State title="Checking connections…" /> : result.isError ? <ErrorBox>{result.error.message}</ErrorBox> : result.data ? <div className="settings-list">
    <ConnectionRow name="Reasoning provider" description="Structured triage and remediation proposals" data={result.data.reasoning} testPath="reasoning" />
    <ConnectionRow name="Grafana" description="Metrics and logs used as incident evidence" data={result.data.grafana} />
    <ConnectionRow name="Prometheus" description="Workload metrics, recording rules, and alert evaluation" data={result.data.prometheus} testPath="prometheus" />
    <ConnectionRow name="Alertmanager" description="Alert routing and delivery status for the playground stack" data={result.data.alertmanager} testPath="alertmanager" />
  </div> : <Empty title="Connections unavailable" text="The server did not return integration status." />}</div>
}

function Settings() {
  const client = useQueryClient(), result = useQuery({ queryKey: ['settings'], queryFn: () => api<Record<string, unknown>>('/api/v1/settings') })
  const update = useMutation({ mutationFn: (enabled: boolean) => api('/api/v1/settings', { method: 'PATCH', body: JSON.stringify({ enabled }) }), onSuccess: () => client.invalidateQueries({ queryKey: ['settings'] }) })
  const enabled = Boolean(result.data?.kill_switch_enabled)
  return <div className="standard-page"><PageHeader title="Settings" subtitle="Deployment posture and execution safety controls." />{result.isLoading ? <State title="Loading settings…" /> : result.isError ? <ErrorBox>{result.error.message}</ErrorBox> : <div className="settings-list"><SettingRow title="Deployment mode" description="Controls authentication and external endpoint policy"><span className="setting-value">{humanize(String(result.data?.deployment_mode || 'unknown'))}</span></SettingRow><SettingRow title="Environment" description="Default environment attached to operational events"><span className="setting-value">{String(result.data?.environment || 'unknown')}</span></SettingRow><SettingRow title="Execution kill switch" description="Persistently blocks new approvals and executions."><div className="setting-action"><Status value={enabled ? 'enabled' : 'disabled'} /><button className={enabled ? '' : 'danger'} disabled={update.isPending} onClick={() => update.mutate(!enabled)}>{enabled ? 'Disable' : 'Enable'}</button></div></SettingRow></div>}{update.isError && <ErrorBox>{update.error.message}</ErrorBox>}</div>
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

function Icon({ name }: { name: IconName }) {
  const paths: Record<IconName, ReactNode> = {
    incident: <><path d="M12 3v5"/><path d="m9.5 6 2.5 2 2.5-2"/><rect x="4" y="11" width="16" height="9" rx="2"/><path d="M8 15h.01M12 15h4"/></>, approval: <><path d="M9 11l2 2 4-4"/><path d="M12 3 4.5 6v5c0 4.6 3.2 8.1 7.5 10 4.3-1.9 7.5-5.4 7.5-10V6L12 3Z"/></>, connection: <><path d="M8 12h8M12 8v8"/><path d="M7 5H5a2 2 0 0 0-2 2v2M17 5h2a2 2 0 0 1 2 2v2M7 19H5a2 2 0 0 1-2-2v-2M17 19h2a2 2 0 0 0 2-2v-2"/></>, settings: <><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1-2.8 2.8-.1-.1a1.7 1.7 0 0 0-1.9-.3 1.7 1.7 0 0 0-1 1.6v.2h-4V21a1.7 1.7 0 0 0-1-1.6 1.7 1.7 0 0 0-1.9.3l-.1.1L4.2 17l.1-.1a1.7 1.7 0 0 0 .3-1.9A1.7 1.7 0 0 0 3 14H2.8v-4H3a1.7 1.7 0 0 0 1.6-1 1.7 1.7 0 0 0-.3-1.9L4.2 7 7 4.2l.1.1A1.7 1.7 0 0 0 9 4.6a1.7 1.7 0 0 0 1-1.6v-.2h4V3a1.7 1.7 0 0 0 1 1.6 1.7 1.7 0 0 0 1.9-.3l.1-.1L19.8 7l-.1.1a1.7 1.7 0 0 0-.3 1.9 1.7 1.7 0 0 0 1.6 1h.2v4H21a1.7 1.7 0 0 0-1.6 1Z"/></>, search: <><circle cx="11" cy="11" r="6"/><path d="m16 16 4 4"/></>, arrow: <path d="m9 18 6-6-6-6"/>, clock: <><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/></>, target: <><circle cx="12" cy="12" r="8"/><circle cx="12" cy="12" r="3"/></>, shield: <path d="M12 3 5 6v5c0 4.4 2.8 7.7 7 9.7 4.2-2 7-5.3 7-9.7V6l-7-3Z"/>, activity: <path d="M3 12h4l2-6 4 12 2-6h6"/>, logout: <><path d="M10 5H5v14h5M14 8l4 4-4 4M18 12H9"/></>,
  }
  return <svg className="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden>{paths[name]}</svg>
}

function humanize(value: string) { return value.replaceAll('_', ' ').replace(/\b\w/g, (letter) => letter.toUpperCase()) }
function verb(value: string) { return value.includes('restart') ? 'restart' : value.includes('pause') ? 'pause' : value.includes('resume') ? 'resume' : 'action' }
function safeJSON(value: unknown) { if (typeof value !== 'string') return value; try { return JSON.parse(value) } catch { return value } }
function stringArray(value: unknown) { return Array.isArray(value) ? value.map(String).filter(Boolean) : [] }
