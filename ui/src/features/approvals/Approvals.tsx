import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { Action, api, Incident } from '../../api'
import { Empty, ErrorBox, Icon, PageHeader, RelativeTime, State, Status } from '../../shared/components'
import { humanize } from '../../shared/format'

export function Approvals() {
  const result = useQuery({ queryKey: ['approvals'], queryFn: () => api<{ items: { incident: Incident; action: Action }[] }>('/api/v1/approvals') }), items = result.data?.items ?? []
  return <div className="standard-page"><PageHeader title="Approvals" subtitle="Actions waiting for an accountable operator decision." end={<span className="result-count">{items.length} waiting</span>} />{result.isLoading ? <State title="Loading approvals…" /> : result.isError ? <ErrorBox>{result.error.message}</ErrorBox> : !items.length ? <Empty title="Approval queue is clear" text="New remediation proposals will appear here when operator review is required." /> : <div className="approval-list">{items.map(({ incident, action }) => <Link className="approval-row" key={action.ID} to={`/ui/incidents/${incident.ID}`}><span className={`severity-line severity-line-${incident.Severity}`} /><div><span className="eyebrow">{humanize(action.ActionType)}</span><strong>{incident.Title}</strong><small>{action.TargetResource}</small></div><Status value={action.RiskLevel} /><RelativeTime value={action.CreatedAt} /><Icon name="arrow" /></Link>)}</div>}</div>
}
