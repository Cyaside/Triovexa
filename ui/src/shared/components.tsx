import { ReactNode } from 'react'
import { APIError } from '../api'
import { humanize } from './format'

export type IconName = 'incident' | 'approval' | 'connection' | 'playground' | 'settings' | 'search' | 'arrow' | 'clock' | 'target' | 'shield' | 'activity' | 'logout'

export function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) { return <label className="form-field"><span>{label}</span>{children}{hint && <small>{hint}</small>}</label> }

export function MutationFeedback({ mutation, success }: { mutation: { isError: boolean; error: Error | null; isSuccess: boolean }; success: string }) { return <div className="mutation-feedback" aria-live="polite">{mutation.isError ? <RequestState error={mutation.error} /> : mutation.isSuccess ? <p className="success-message">{success}</p> : null}</div> }

export function SectionHeading({ title, meta }: { title: string; meta?: string }) { return <header className="section-heading"><h2>{title}</h2>{meta && <span>{meta}</span>}</header> }

export function PageHeader({ title, subtitle, end }: { title: string; subtitle: string; end?: ReactNode }) { return <header className="page-header"><div><p className="eyebrow">Triovexa / Operations</p><h1>{title}</h1><p>{subtitle}</p></div>{end}</header> }

export function Status({ value }: { value: unknown }) { const text = String(value || 'unknown'); return <span className={`status status-${text.replaceAll('_', '-')}`}><i />{humanize(text)}</span> }

export function RelativeTime({ value }: { value: string }) { const date = new Date(value); if (Number.isNaN(date.valueOf())) return <time>Unknown time</time>; const minutes = Math.round((Date.now() - date.valueOf()) / 60000); const label = minutes < 1 ? 'just now' : minutes < 60 ? `${minutes}m ago` : minutes < 1440 ? `${Math.round(minutes / 60)}h ago` : `${Math.round(minutes / 1440)}d ago`; return <time dateTime={value} title={date.toLocaleString()}>{label}</time> }

export function Count({ value }: { value: number }) { return <span className="count">{value}</span> }

export function Empty({ title, text, compact = false }: { title: string; text: string; compact?: boolean }) { return <div className={`empty${compact ? ' compact' : ''}`}><strong>{title}</strong><p>{text}</p></div> }

export function State({ title }: { title: string }) { return <div className="state"><Spinner />{title}</div> }

export function Spinner() { return <span className="spinner" aria-hidden /> }

export function Centered({ children }: { children: ReactNode }) { return <main className="centered">{children}</main> }

export function ErrorBox({ children }: { children: ReactNode }) { return <div className="error" role="alert">{children}</div> }

export function RequestState({ error, retry }: { error: unknown; retry?: () => void }) {
  const apiError = error instanceof APIError ? error : null
  if (apiError?.status === 403) return <SystemState kind="forbidden" title="Access denied" text="Your account does not have permission to perform this operation." retry={retry} />
  if (apiError?.status === 409) return <SystemState kind="conflict" title="State changed on the server" text="Another operator or workflow updated this record. Refresh before deciding again." retry={retry} />
  if (!apiError && error) return <SystemState kind="disconnected" title="Connection lost" text="Triovexa could not be reached. Check the server and retry." retry={retry} />
  return <SystemState kind="error" title="Request failed" text={error instanceof Error ? error.message : 'The requested data is unavailable.'} retry={retry} />
}

export function SystemState({ kind, title, text, retry, compact = false }: { kind: string; title: string; text: string; retry?: () => void; compact?: boolean }) { return <section className={`system-state system-state-${kind}${compact ? ' compact' : ''}`} role={kind === 'error' || kind === 'forbidden' || kind === 'conflict' ? 'alert' : 'status'}><div><strong>{title}</strong><p>{text}</p></div>{retry && <button onClick={retry}>{kind === 'conflict' || kind === 'stale' ? 'Refresh' : 'Retry'}</button>}</section> }

export function Icon({ name }: { name: IconName }) {
  const paths: Record<IconName, ReactNode> = {
    incident: <><path d="M12 3v5"/><path d="m9.5 6 2.5 2 2.5-2"/><rect x="4" y="11" width="16" height="9" rx="2"/><path d="M8 15h.01M12 15h4"/></>, approval: <><path d="M9 11l2 2 4-4"/><path d="M12 3 4.5 6v5c0 4.6 3.2 8.1 7.5 10 4.3-1.9 7.5-5.4 7.5-10V6L12 3Z"/></>, connection: <><path d="M8 12h8M12 8v8"/><path d="M7 5H5a2 2 0 0 0-2 2v2M17 5h2a2 2 0 0 1 2 2v2M7 19H5a2 2 0 0 1-2-2v-2M17 19h2a2 2 0 0 0 2-2v-2"/></>, playground: <><path d="M6 4h12v5a6 6 0 0 1-12 0V4Z"/><path d="M9 15v5M15 15v5M7 20h10"/></>, settings: <><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1-2.8 2.8-.1-.1a1.7 1.7 0 0 0-1.9-.3 1.7 1.7 0 0 0-1 1.6v.2h-4V21a1.7 1.7 0 0 0-1-1.6 1.7 1.7 0 0 0-1.9.3l-.1.1L4.2 17l.1-.1a1.7 1.7 0 0 0 .3-1.9A1.7 1.7 0 0 0 3 14H2.8v-4H3a1.7 1.7 0 0 0 1.6-1 1.7 1.7 0 0 0-.3-1.9L4.2 7 7 4.2l.1.1A1.7 1.7 0 0 0 9 4.6a1.7 1.7 0 0 0 1-1.6v-.2h4V3a1.7 1.7 0 0 0 1 1.6 1.7 1.7 0 0 0 1.9-.3l.1-.1L19.8 7l-.1.1a1.7 1.7 0 0 0-.3 1.9 1.7 1.7 0 0 0 1.6 1h.2v4H21a1.7 1.7 0 0 0-1.6 1Z"/></>, search: <><circle cx="11" cy="11" r="6"/><path d="m16 16 4 4"/></>, arrow: <path d="m9 18 6-6-6-6"/>, clock: <><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/></>, target: <><circle cx="12" cy="12" r="8"/><circle cx="12" cy="12" r="3"/></>, shield: <path d="M12 3 5 6v5c0 4.4 2.8 7.7 7 9.7 4.2-2 7-5.3 7-9.7V6l-7-3Z"/>, activity: <path d="M3 12h4l2-6 4 12 2-6h6"/>, logout: <><path d="M10 5H5v14h5M14 8l4 4-4 4M18 12H9"/></>,
  }
  return <svg className="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden>{paths[name]}</svg>
}
