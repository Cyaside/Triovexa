import { FormEvent, ReactNode, useState } from 'react'
import * as Tabs from '@radix-ui/react-tabs'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, NavLink, Navigate, Route, Routes, useLocation, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { Action, api, Detail, Incident } from './api'

type User = { id: string; username: string; role: string }

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
    event.preventDefault(); const data = new FormData(event.currentTarget)
    mutation.mutate({ Username: String(data.get('username')), Password: String(data.get('password')) })
  }
  return <main className="login-page"><form className="login-panel" onSubmit={submit}>
    <Brand /><h1>Operator sign in</h1><p className="muted">Use an account created by a Triovexa administrator.</p>
    <label>Username<input name="username" autoComplete="username" required autoFocus /></label>
    <label>Password<input name="password" type="password" autoComplete="current-password" required /></label>
    {error && <ErrorBox>{error}</ErrorBox>}<button className="primary" disabled={mutation.isPending}>{mutation.isPending ? 'Signing in…' : 'Sign in'}</button>
  </form></main>
}

function Shell({ user }: { user: User }) {
  const navigate = useNavigate()
  const logout = useMutation({ mutationFn: () => api('/api/v1/session/logout', { method: 'DELETE' }), onSuccess: () => location.reload() })
  return <div className="shell"><aside><Brand /><nav>
    <NavLink to="/ui/incidents">Incidents</NavLink><NavLink to="/ui/approvals">Approvals</NavLink><NavLink to="/ui/connections">Connections</NavLink><NavLink to="/ui/settings">Settings</NavLink>
  </nav><div className="account"><span>{user.username}</span><small>{user.role}</small><button className="text" onClick={() => logout.mutate()}>Sign out</button></div></aside>
  <main className="content"><Routes><Route path="/ui/incidents" element={<Incidents />} /><Route path="/ui/incidents/:id" element={<IncidentDetail />} /><Route path="/ui/approvals" element={<Approvals />} /><Route path="/ui/connections" element={<Connections />} /><Route path="/ui/settings" element={<Settings />} /><Route path="*" element={<Navigate to="/ui/incidents" replace />} /></Routes></main></div>
}

function Brand() { return <div className="brand"><span className="brand-mark">T</span><span>Triovexa</span></div> }

function Incidents() {
  const [params, setParams] = useSearchParams(); const query = params.toString()
  const incidents = useQuery({ queryKey: ['incidents', query], queryFn: () => api<{ items: Incident[]; total: number }>(`/api/v1/incidents?${query}`) })
  const update = (key: string, value: string) => { const next = new URLSearchParams(params); value ? next.set(key, value) : next.delete(key); next.delete('page'); setParams(next) }
  const payload = incidents.data
  return <><PageHeader title="Incidents" subtitle="Prioritized operational events and current recovery state." />
    <div className="toolbar"><input aria-label="Search incidents" placeholder="Search title, service, or ID" value={params.get('q') ?? ''} onChange={(e) => update('q', e.target.value)} />
      <select aria-label="Status" value={params.get('status') ?? ''} onChange={(e) => update('status', e.target.value)}><option value="">All statuses</option><option value="awaiting_approval">Awaiting approval</option><option value="executing_action">Executing</option><option value="resolved">Resolved</option><option value="escalated">Escalated</option></select>
      <select aria-label="Severity" value={params.get('severity') ?? ''} onChange={(e) => update('severity', e.target.value)}><option value="">All severities</option><option>critical</option><option>high</option><option>medium</option><option>low</option></select>
    </div>
    {incidents.isLoading ? <State title="Loading incidents…" /> : incidents.isError ? <ErrorBox>{incidents.error.message}</ErrorBox> : !payload || payload.items.length === 0 ? <State title="No incidents match these filters" /> : <div className="table-wrap"><table><thead><tr><th>Incident</th><th>Service</th><th>Severity</th><th>Status</th><th>Environment</th><th>Updated</th></tr></thead><tbody>{payload.items.map((item) => <tr key={item.ID}><td><Link className="incident-link" to={`/ui/incidents/${item.ID}`}>{item.Title}</Link><code>{item.ID.slice(0, 12)}</code></td><td>{item.ServiceName}</td><td><Status value={item.Severity} /></td><td><Status value={item.State} /></td><td>{item.Environment}</td><td><Time value={item.UpdatedAt} /></td></tr>)}</tbody></table></div>}
  </>
}

function IncidentDetail() {
  const { id = '' } = useParams(); const client = useQueryClient()
  const detail = useQuery({ queryKey: ['incident', id], queryFn: () => api<Detail>(`/api/v1/incidents/${id}`) })
  const actionMutation = useMutation({ mutationFn: ({ action, operation }: { action: Action; operation: string }) => api(`/api/v1/actions/${action.ID}/${operation}`, { method: 'POST', body: '{}' }), onSuccess: () => client.invalidateQueries({ queryKey: ['incident', id] }) })
  if (detail.isLoading) return <State title="Loading incident…" />
  if (detail.isError || !detail.data) return <ErrorBox>{detail.error?.message ?? 'Incident data is unavailable.'}</ErrorBox>
  const data = detail.data
  return <><div className="detail-header"><div><Link className="back" to="/ui/incidents">← Incidents</Link><h1>{data.incident.Title}</h1><div className="meta"><Status value={data.incident.State} /><span>{data.incident.ServiceName}</span><span>{data.incident.Environment}</span><code>{data.incident.ID}</code></div></div><Time value={data.incident.UpdatedAt} /></div>
    {actionMutation.isError && <ErrorBox>{actionMutation.error.message}</ErrorBox>}
    <div className="detail-layout"><Tabs.Root className="tabs" defaultValue="overview"><Tabs.List><Tabs.Trigger value="overview">Overview</Tabs.Trigger><Tabs.Trigger value="evidence">Evidence <Count value={data.evidence.length} /></Tabs.Trigger><Tabs.Trigger value="actions">Actions <Count value={data.candidate_actions.length} /></Tabs.Trigger><Tabs.Trigger value="activity">Activity <Count value={data.audit_events.length} /></Tabs.Trigger></Tabs.List>
      <Tabs.Content value="overview"><Panel title="Triage summary">{data.triage ? <ObjectView value={data.triage} /> : <Empty text="Triage is still running or unavailable." />}</Panel><Panel title="Recovery"><VerificationList items={data.verification_results} /></Panel></Tabs.Content>
      <Tabs.Content value="evidence"><EvidenceList items={data.evidence} /></Tabs.Content>
      <Tabs.Content value="actions"><ActionList actions={data.candidate_actions} mutate={(action, operation) => actionMutation.mutate({ action, operation })} busy={actionMutation.isPending} /></Tabs.Content>
      <Tabs.Content value="activity"><Timeline items={data.audit_events} /></Tabs.Content>
    </Tabs.Root><aside className="action-panel"><h2>Operator actions</h2><p className="muted">Review evidence and scope before approval. Execution begins only after server confirmation.</p><ActionList actions={data.candidate_actions.filter((a) => ['awaiting_approval','approved','executing'].includes(a.Status))} mutate={(action, operation) => actionMutation.mutate({ action, operation })} busy={actionMutation.isPending} compact /></aside></div>
  </>
}

function ActionList({ actions, mutate, busy, compact = false }: { actions: Action[]; mutate: (a: Action, op: string) => void; busy: boolean; compact?: boolean }) {
  if (!actions.length) return <Empty text="No operator action is currently available." />
  return <div className="stack">{actions.map((action) => <article className="action-card" key={action.ID}><div className="row"><strong>{humanize(action.ActionType)}</strong><Status value={action.Status} /></div><p>{action.Rationale}</p><dl><dt>Target</dt><dd><code>{action.TargetResource}</code></dd><dt>Risk</dt><dd>{action.RiskLevel}</dd>{!compact && <><dt>Parameters</dt><dd><code>{action.ParametersJSON}</code></dd></>}</dl><div className="button-row">{action.Status === 'awaiting_approval' && <><button className="primary" disabled={busy} onClick={() => mutate(action, 'approve')}>Approve {verb(action.ActionType)}</button><button disabled={busy} onClick={() => mutate(action, 'reject')}>Reject</button></>}{action.Status === 'approved' && <button className="danger" disabled={busy} onClick={() => mutate(action, 'execute')}>Execute {verb(action.ActionType)}</button>}</div></article>)}</div>
}

function Approvals() {
  const result = useQuery({ queryKey: ['approvals'], queryFn: () => api<{ items: { incident: Incident; action: Action }[] }>('/api/v1/approvals') })
  const items = result.data?.items ?? []
  return <><PageHeader title="Approvals" subtitle="Actions waiting for an accountable operator decision." />{result.isLoading ? <State title="Loading approvals…" /> : result.isError ? <ErrorBox>{result.error.message}</ErrorBox> : !items.length ? <State title="No approvals are waiting" /> : <div className="stack">{items.map(({ incident, action }) => <Link className="approval-row" key={action.ID} to={`/ui/incidents/${incident.ID}`}><div><strong>{humanize(action.ActionType)}</strong><span>{incident.Title}</span></div><div><Status value={action.RiskLevel} /><span>{action.TargetResource}</span></div></Link>)}</div>}</>
}

function Connections() {
  const result = useQuery({ queryKey: ['connections'], queryFn: () => api<{ reasoning: Record<string, unknown>; grafana: Record<string, unknown> }>('/api/v1/connections') })
  const test = useMutation({ mutationFn: () => api<Record<string, unknown>>('/api/v1/connections/reasoning/test', { method: 'POST', body: '{}' }) })
  return <><PageHeader title="Connections" subtitle="Status for server-managed credentials and integrations." />{result.isLoading ? <State title="Checking connections…" /> : result.isError ? <ErrorBox>{result.error.message}</ErrorBox> : result.data ? <div className="card-grid"><ConnectionCard name="Reasoning provider" data={result.data.reasoning} action={<button onClick={() => test.mutate()} disabled={test.isPending}>{test.isPending ? 'Testing…' : 'Test connection'}</button>} /><ConnectionCard name="Grafana" data={result.data.grafana} /></div> : <State title="Connection status is unavailable" />}{test.isError && <ErrorBox>{test.error.message}</ErrorBox>}{test.data && <Panel title="Connection test"><ObjectView value={test.data} /></Panel>}</>
}

function Settings() {
  const client = useQueryClient(); const result = useQuery({ queryKey: ['settings'], queryFn: () => api<Record<string, unknown>>('/api/v1/settings') })
  const update = useMutation({ mutationFn: (enabled: boolean) => api('/api/v1/settings', { method: 'PATCH', body: JSON.stringify({ enabled }) }), onSuccess: () => client.invalidateQueries({ queryKey: ['settings'] }) })
  const enabled = Boolean(result.data?.kill_switch_enabled)
  return <><PageHeader title="Settings" subtitle="Deployment posture and safety controls." />{result.isLoading ? <State title="Loading settings…" /> : result.isError ? <ErrorBox>{result.error.message}</ErrorBox> : <><Panel title="Runtime"><ObjectView value={result.data} /></Panel><Panel title="Execution safety"><div className="row"><div><strong>Kill switch</strong><p className="muted">Persistently block new approvals and executions.</p></div><button className={enabled ? '' : 'danger'} disabled={update.isPending} onClick={() => update.mutate(!enabled)}>{enabled ? 'Disable kill switch' : 'Enable kill switch'}</button></div></Panel></>}{update.isError && <ErrorBox>{update.error.message}</ErrorBox>}</>
}

function ConnectionCard({ name, data, action }: { name: string; data: Record<string, unknown>; action?: ReactNode }) { return <article className="connection-card"><div className="row"><h2>{name}</h2><Status value={data.configured ? 'connected' : 'not configured'} /></div><ObjectView value={data} />{action && <div className="button-row">{action}</div>}</article> }
function EvidenceList({ items }: { items: Record<string, unknown>[] }) { return items.length ? <div className="stack">{items.map((item, index) => <article className="evidence" key={String(item.ID ?? index)}><div className="row"><strong>{String(item.Type ?? 'Evidence')}</strong><span>{String(item.Source ?? '')}</span></div><p>{String(item.Snippet ?? '')}</p><ObjectView value={safeJSON(item.MetadataJSON)} /></article>)}</div> : <Empty text="No evidence has been collected." /> }
function VerificationList({ items }: { items: Record<string, unknown>[] }) { return items.length ? <div className="stack">{items.map((item, index) => <div className="verification" key={String(item.ID ?? index)}><Status value={String(item.Status)} /><span>{String(item.Notes ?? '')}</span></div>)}</div> : <Empty text="No recovery verification has run yet." /> }
function Timeline({ items }: { items: Record<string, unknown>[] }) { return items.length ? <ol className="timeline">{[...items].reverse().map((item, index) => <li key={String(item.ID ?? index)}><span className="dot" /><div><div className="row"><strong>{humanize(String(item.StepName ?? 'event'))}</strong><Status value={String(item.Status ?? '')} /></div><Time value={String(item.StartedAt ?? '')} /><details><summary>Details</summary><ObjectView value={safeJSON(item.DetailsJSON)} /></details></div></li>)}</ol> : <Empty text="No activity recorded." /> }
function ObjectView({ value }: { value: unknown }) { return <pre>{JSON.stringify(value, null, 2)}</pre> }
function Panel({ title, children }: { title: string; children: ReactNode }) { return <section className="panel"><h2>{title}</h2>{children}</section> }
function PageHeader({ title, subtitle }: { title: string; subtitle: string }) { return <header className="page-header"><h1>{title}</h1><p>{subtitle}</p></header> }
function Status({ value }: { value: unknown }) { const text = String(value || 'unknown'); return <span className={`status status-${text.replaceAll('_','-')}`}>{humanize(text)}</span> }
function Time({ value }: { value: string }) { const date = new Date(value); return <time dateTime={value}>{Number.isNaN(date.valueOf()) ? 'Unknown time' : date.toLocaleString()}</time> }
function Count({ value }: { value: number }) { return <span className="count">{value}</span> }
function Empty({ text }: { text: string }) { return <p className="empty">{text}</p> }
function State({ title }: { title: string }) { return <div className="state"><Spinner />{title}</div> }
function Spinner() { return <span className="spinner" aria-hidden /> }
function Centered({ children }: { children: ReactNode }) { return <main className="centered">{children}</main> }
function ErrorBox({ children }: { children: ReactNode }) { return <div className="error" role="alert">{children}</div> }
function humanize(value: string) { return value.replaceAll('_', ' ').replace(/\b\w/g, (letter) => letter.toUpperCase()) }
function verb(value: string) { return value.includes('restart') ? 'restart' : value.includes('pause') ? 'pause' : value.includes('resume') ? 'resume' : 'action' }
function safeJSON(value: unknown) { if (typeof value !== 'string') return value; try { return JSON.parse(value) } catch { return value } }
