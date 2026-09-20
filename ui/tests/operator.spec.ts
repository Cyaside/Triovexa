import { expect, test } from '@playwright/test'

const incident = {
  ID: 'incident-123456789', ExternalAlertID: 'alert-1', AlertSource: 'grafana', Title: 'Queue worker stalled',
  ServiceName: 'queue-worker', Environment: 'staging', Severity: 'high', State: 'awaiting_approval',
  CreatedAt: '2026-09-19T10:00:00Z', UpdatedAt: '2026-09-19T10:01:00Z',
}

test.beforeEach(async ({ page }) => {
  await page.route('**/api/v1/session', (route) => route.fulfill({ json: { user: { id: 'u1', username: 'operator', role: 'operator' } } }))
  await page.route('**/api/v1/incidents?**', (route) => route.fulfill({ json: { items: [incident], page: 1, page_size: 25, total: 1 } }))
  await page.route('**/api/v1/incidents/incident-123456789', (route) => route.fulfill({ json: {
    incident, triage: { Summary: 'Worker heartbeat stopped while backlog increased.' },
    evidence: [{ ID: 'e1', Type: 'metric', Source: 'workload-control', Snippet: 'worker stalled; queue backlog=42', MetadataJSON: '{"queue_backlog":42}' }],
    documents: [], candidate_actions: [{ ID: 'a1', IncidentID: incident.ID, ActionType: 'restart_demo_worker', TargetResource: 'demo-worker', ParametersJSON: '{"worker_id":"worker-primary"}', RiskLevel: 'low', Rationale: 'Restore queue progress.', EvidenceRefs: ['e1'], ApprovalHint: '', Status: 'awaiting_approval', CreatedAt: incident.UpdatedAt }],
    policy_decisions: [], approval_records: [], execution_records: [], verification_results: [], rollback_records: [],
    audit_events: [{ ID: 'audit-1', StepName: 'webhook_intake', Status: 'completed', StartedAt: incident.CreatedAt, DetailsJSON: '{}' }],
  } }))
  await page.route('**/api/v1/actions/a1/approve', (route) => route.fulfill({ json: { status: 'accepted' } }))
  await page.route('**/api/v1/connections', (route) => route.fulfill({ json: {
    reasoning: { configured: false, provider: 'mistral', model: 'mistral-small-latest' },
    grafana: { configured: true, metrics_source_uid: 'prometheus', logs_source_uid: 'loki' },
    prometheus: { configured: true, endpoint: 'http://prometheus:9090' },
    alertmanager: { configured: true, endpoint: 'http://alertmanager:9093' },
  } }))
  await page.route('**/api/v1/connections/prometheus/test', (route) => route.fulfill({ json: { status: 'connected', service: 'prometheus', latency_ms: 12 } }))
})

test('filters incidents using URL state', async ({ page }) => {
  await page.goto('/ui/incidents')
  await expect(page.getByRole('heading', { name: 'Incidents' })).toBeVisible()
  await expect(page.getByText('Queue worker stalled')).toBeVisible()
  await page.getByLabel('Search incidents').fill('queue')
  await expect(page).toHaveURL(/q=queue/)
})

test('reviews evidence and sends explicit approval', async ({ page }) => {
  await page.goto('/ui/incidents/incident-123456789')
  await page.getByRole('tab', { name: /Evidence/ }).click()
  await expect(page.getByText('worker stalled; queue backlog=42')).toBeVisible()
  const approval = page.waitForRequest((request) => request.url().endsWith('/api/v1/actions/a1/approve') && request.method() === 'POST')
  await page.getByRole('button', { name: 'Approve restart' }).first().click()
  await approval
})

test('keeps the operator workspace inside the viewport', async ({ page }) => {
  await page.goto('/ui/incidents')
  await expect(page.getByRole('heading', { name: 'Incidents' })).toBeVisible()
  const incidentOverflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(incidentOverflow).toBeLessThanOrEqual(1)

  await page.goto('/ui/incidents/incident-123456789')
  await expect(page.getByRole('heading', { name: 'Queue worker stalled' })).toBeVisible()
  await expect(page.getByText('Worker heartbeat stopped while backlog increased.')).toBeVisible()
  const detailOverflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(detailOverflow).toBeLessThanOrEqual(1)
})

test('shows and tests direct monitoring connections', async ({ page }) => {
  await page.goto('/ui/connections')
  await expect(page.getByRole('heading', { name: 'Prometheus' })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Alertmanager' })).toBeVisible()
  const request = page.waitForRequest((item) => item.url().endsWith('/api/v1/connections/prometheus/test'))
  await page.getByRole('button', { name: 'Test connection' }).nth(1).click()
  await request
  await expect(page.getByText('12 ms')).toBeVisible()
})
