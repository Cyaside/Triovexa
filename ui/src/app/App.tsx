import { FormEvent, ReactNode, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Link, NavLink, Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { api } from '../api'
import { User } from '../shared/types'
import { Centered, ErrorBox, Icon, IconName, Spinner } from '../shared/components'
import { Incidents } from '../features/incidents/Incidents'
import { IncidentDetail } from '../features/incidents/IncidentDetail'
import { Approvals } from '../features/approvals/Approvals'
import { Connections } from '../features/connections/Connections'
import { Playground } from '../features/playground/Playground'
import { Settings } from '../features/settings/Settings'

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
      <Route path="/ui/incidents" element={<Incidents />} /><Route path="/ui/incidents/:id" element={<IncidentDetail user={user} />} /><Route path="/ui/approvals" element={<Approvals />} /><Route path="/ui/connections" element={<Connections />} /><Route path="/ui/playground" element={<Playground />} /><Route path="/ui/settings" element={<Settings />} /><Route path="*" element={<Navigate to="/ui/incidents" replace />} />
    </Routes></main>
  </div>
}

function Brand() { return <Link className="brand" to="/ui/incidents" aria-label="Triovexa home"><span className="brand-mark" aria-hidden><i /><i /><i /></span><span>Triovexa</span></Link> }

function NavItem({ to, icon, children }: { to: string; icon: IconName; children: ReactNode }) { return <NavLink to={to}><Icon name={icon} /><span>{children}</span></NavLink> }
