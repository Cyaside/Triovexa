import * as Tabs from '@radix-ui/react-tabs'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useLocation, useParams } from 'react-router-dom'
import { Action, api, Detail } from '../../api'
import { User } from '../../shared/types'
import { Count, Empty, Icon, RelativeTime, RequestState, SectionHeading, State, Status, SystemState } from '../../shared/components'
import { humanize, safeJSON, stringArray, verb } from '../../shared/format'
import { CodeRepairPanel } from '../code-repair/CodeRepair'

export function IncidentDetail({ user }: { user: User }) {
  const { id = '' } = useParams()
  const location = useLocation()
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
    <div className="incident-body"><Tabs.Root className="investigation" defaultValue={new URLSearchParams(location.search).get('tab') === 'code-repair' ? 'code-repair' : 'overview'}><Tabs.List aria-label="Incident information"><Tabs.Trigger value="overview">Overview</Tabs.Trigger><Tabs.Trigger value="evidence">Evidence <Count value={data.evidence.length} /></Tabs.Trigger><Tabs.Trigger value="actions">Actions <Count value={data.candidate_actions.length} /></Tabs.Trigger><Tabs.Trigger value="code-repair">Code repair</Tabs.Trigger><Tabs.Trigger value="activity">Activity <Count value={data.audit_events.length} /></Tabs.Trigger></Tabs.List>
      <Tabs.Content value="overview"><TriageSummary triage={data.triage} /><SectionHeading title="Recovery status" meta={data.verification_results.length ? `${data.verification_results.length} checks` : 'Waiting for execution'} /><VerificationList items={data.verification_results} /></Tabs.Content>
      <Tabs.Content value="evidence"><EvidenceList items={data.evidence} /></Tabs.Content>
      <Tabs.Content value="actions"><ActionList actions={data.candidate_actions} mutate={(action, operation) => actionMutation.mutate({ action, operation })} busy={actionMutation.isPending} /></Tabs.Content>
      <Tabs.Content value="code-repair"><CodeRepairPanel incident={data.incident} user={user} /></Tabs.Content>
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
