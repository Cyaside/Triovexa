import { useQuery } from '@tanstack/react-query'
import { Link, useSearchParams } from 'react-router-dom'
import { api, Incident } from '../../api'
import { Count, Empty, Icon, PageHeader, RelativeTime, RequestState, State, Status, SystemState } from '../../shared/components'
import { humanize } from '../../shared/format'

export function Incidents() {
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
