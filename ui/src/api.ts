export type Incident = {
  ID: string; ExternalAlertID: string; AlertSource: string; Title: string; ServiceName: string
  Environment: string; Severity: string; State: string; CreatedAt: string; UpdatedAt: string
}
export type Action = {
  ID: string; IncidentID: string; ActionType: string; TargetResource: string; ParametersJSON: string
  RiskLevel: string; Rationale: string; EvidenceRefs: string[]; ApprovalHint: string; Status: string; CreatedAt: string
}
export type Detail = {
  incident: Incident; triage: null | Record<string, unknown>; evidence: Record<string, unknown>[]
  documents: Record<string, unknown>[]; candidate_actions: Action[]; policy_decisions: Record<string, unknown>[]
  approval_records: Record<string, unknown>[]; execution_records: Record<string, unknown>[]
  verification_results: Record<string, unknown>[]; rollback_records: Record<string, unknown>[]; audit_events: Record<string, unknown>[]
}

function csrfToken() {
  const item = document.cookie.split('; ').find((value) => value.startsWith('triovexa_csrf='))
  return item ? decodeURIComponent(item.split('=').slice(1).join('=')) : ''
}

export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers)
  if (init?.body) headers.set('Content-Type', 'application/json')
  const csrf = csrfToken()
  if (csrf) headers.set('X-CSRF-Token', csrf)
  const response = await fetch(path, { ...init, headers, credentials: 'same-origin' })
  if (!response.ok) {
    const payload = await response.json().catch(() => null)
    throw new Error(payload?.error?.message ?? `Request failed (${response.status})`)
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}
