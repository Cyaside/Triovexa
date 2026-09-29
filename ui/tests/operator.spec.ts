import { expect, test } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

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
    reasoning: { configured: false, provider: 'openai-compatible', model: '' },
    grafana: { configured: true, metrics_source_uid: 'prometheus', logs_source_uid: 'loki' },
    prometheus: { configured: true, endpoint: 'http://prometheus:9090' },
    alertmanager: { configured: true, endpoint: 'http://alertmanager:9093' },
    loki: { configured: true, endpoint: 'http://loki:3100' },
  } }))
  await page.route('**/api/v1/connections/prometheus/test', (route) => route.fulfill({ json: { status: 'connected', service: 'prometheus', latency_ms: 12 } }))
  await page.route('**/api/v1/connections/reasoning/config', (route) => route.fulfill({ json: { provider: 'openai-compatible', base_url: 'https://api.openai.com/v1', model: 'gpt-4.1-mini', credential_ref: 'LLM_API_KEY', credential_available: route.request().method() === 'PUT', credential_source: route.request().method() === 'PUT' ? 'runtime_memory' : 'missing', json_mode: true } }))
  await page.route('**/api/v1/connections/grafana/config', (route) => route.fulfill({ json: { base_url: 'http://grafana:3000', credential_ref: 'GRAFANA_API_TOKEN', credential_available: false, metrics_source_uid: 'prometheus', logs_source_uid: 'loki', error_rate_query: '', latency_query: '', queue_query: 'triovexa_queue_backlog', replica_query: '', logs_query: '', deploy_logs_query: '' } }))
  await page.route('**/api/v1/settings', (route) => route.fulfill({ json: { deployment_mode: 'local-demo', environment: 'local', kill_switch_enabled: false, runtime: { reasoning: 'heuristic', observability: 'demo' } } }))
  await page.route('**/api/v1/playground', (route) => route.fulfill({ json: { enabled: true, modes: ['healthy', 'stall', 'fail'], state: { target: 'queue-worker', worker_healthy: true, queue_backlog: 3, jobs_processed: 24, generation: 1 } } }))
  await page.route('**/api/v1/playground/faults', (route) => route.fulfill({ json: { mode: 'stall' } }))
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

test('supports keyboard entry and the intermediate-width layout', async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 768 })
  await page.goto('/ui/incidents')
  await expect(page.getByRole('heading', { name: 'Incidents' })).toBeVisible()
  await page.keyboard.press('Tab')
  const skipLink = page.getByRole('link', { name: 'Skip to main content' })
  await expect(skipLink).toBeFocused()
  await page.keyboard.press('Enter')
  await expect(page.locator('#main-content')).toBeFocused()
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(overflow).toBeLessThanOrEqual(1)
})

test('shows and tests direct monitoring connections', async ({ page }) => {
  await page.goto('/ui/connections')
  await expect(page.getByRole('heading', { name: 'Prometheus' })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Alertmanager' })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Loki' })).toBeVisible()
  const request = page.waitForRequest((item) => item.url().endsWith('/api/v1/connections/prometheus/test'))
  await page.getByRole('button', { name: 'Test connection' }).nth(1).click()
  await request
  await expect(page.getByText('12 ms')).toBeVisible()
})

test('accepts an OpenAI-compatible API key without displaying it again', async ({ page }) => {
  await page.goto('/ui/connections')
  await expect(page.getByRole('heading', { name: 'OpenAI-compatible API' })).toBeVisible()
  await page.getByLabel('API key').fill('browser-secret')
  const request = page.waitForRequest((item) => item.url().endsWith('/api/v1/connections/reasoning/config') && item.method() === 'PUT')
  await page.getByRole('button', { name: 'Save & activate' }).click()
  expect((await request).postDataJSON().api_key).toBe('browser-secret')
  await expect(page.getByLabel('API key')).toHaveValue('')
  await expect(page.getByText('Provider encrypted, saved, and activated. Reasoning mode is now using the configured LLM.')).toBeVisible()
})

test('injects a bounded worker stall from the playground', async ({ page }) => {
  await page.goto('/ui/playground')
  await expect(page.getByRole('heading', { name: 'Playground' })).toBeVisible()
  const request = page.waitForRequest((item) => item.url().endsWith('/api/v1/playground/faults') && item.postDataJSON().mode === 'stall')
  await page.getByRole('button', { name: 'Inject worker stall' }).click()
  await request
})

test('renders an actionable conflict state for concurrent approval', async ({ page }) => {
  await page.route('**/api/v1/actions/a1/approve', (route) => route.fulfill({ status: 409, json: { error: { code: 'action_rejected', message: 'action state changed' } } }))
  await page.goto('/ui/incidents/incident-123456789')
  await page.getByRole('button', { name: 'Approve restart' }).first().click()
  await expect(page.getByRole('alert').getByText('State changed on the server')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Refresh' })).toBeVisible()
})

test('shows code repair evidence and publishes only the reviewed digest', async ({ page }) => {
  const repairCase = { ID: 'case-123456789', IncidentID: incident.ID, BindingID: 'binding-1', BaseSHA: '1'.repeat(40),
    DeployedSHA: '1'.repeat(40), State: 'awaiting_publish_approval', Version: 4, CreatedBy: 'u1' }
  await page.route('**/api/v1/incidents/incident-123456789', (route) => route.fulfill({ json: {
    incident: { ...incident, State: 'escalated' }, triage: { Summary: 'Worker stopped on an unsupported job schema.' },
    evidence: [], documents: [], candidate_actions: [], policy_decisions: [], approval_records: [],
    execution_records: [], verification_results: [], rollback_records: [], audit_events: [],
  } }))
  await page.route('**/api/v1/repair/incidents/incident-123456789', (route) => route.fulfill({ json: { items: [repairCase] } }))
  await page.route('**/api/v1/repair/cases/case-123456789', (route) => route.fulfill({ json: {
    case: repairCase, binding: { RepositoryURL: 'https://github.com/acme/worker', BaseRef: 'main', AllowedPaths: ['internal/workload'], TestRecipes: ['go-test-workload'] },
    evidence: { captured_at: '2026-09-19T10:00:00Z', sha256: 'e'.repeat(64), entries: [{ id: 'e1', type: 'log', source: 'loki', status: 'complete', text: 'unsupported job schema' }] },
    attempt: { ID: 'attempt-1', Status: 'succeeded', Provider: 'openai-compatible', Model: 'glm-5.3-flash', ErrorCode: '', ErrorMessage: '' },
    report: { status: 'patch_ready', hypothesis: 'Worker rejects schema 2.', recipe_id: 'go-test-workload', before_exit: 1, after_exit: 0, patch_sha256: 'a'.repeat(64), evidence_ids: ['e1'] },
    patch: 'diff --git a/internal/workload/repair_fixture.go b/internal/workload/repair_fixture.go', review_digest: 'd'.repeat(64),
    deployments: [], events: [{ ID: 'event-1', Type: 'publication_review_requested', ActorID: 'u1', DetailsJSON: '{"attempt_id":"attempt-1"}', CreatedAt: '2026-09-19T10:02:00Z' }],
  } }))
  await page.route('**/api/v1/repair/cases/case-123456789/publish', (route) => route.fulfill({ status: 202, json: { state: 'queued' } }))
  await page.goto('/ui/incidents/incident-123456789')
  await page.getByRole('tab', { name: 'Code repair' }).click()
  await page.getByText('loki', { exact: true }).click()
  await expect(page.getByText('unsupported job schema')).toBeVisible()
  await expect(page.getByText('Worker rejects schema 2.')).toBeVisible()
  await expect(page.getByText('diff --git a/internal/workload/repair_fixture.go b/internal/workload/repair_fixture.go')).toBeVisible()
  const accessibility = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
  expect(accessibility.violations.filter((violation) => ['serious', 'critical'].includes(violation.impact ?? ''))).toEqual([])
  const publish = page.waitForRequest((request) => request.url().endsWith('/api/v1/repair/cases/case-123456789/publish'))
  await page.getByRole('button', { name: 'Approve draft PR publication' }).click()
  expect((await publish).postDataJSON()).toEqual({ expected_version: 4, review_digest: 'd'.repeat(64) })
})

for (const path of ['/ui/incidents', '/ui/incidents/incident-123456789', '/ui/connections', '/ui/playground', '/ui/settings']) {
  test(`has no serious accessibility violations on ${path}`, async ({ page }) => {
    await page.goto(path)
    await page.waitForLoadState('networkidle')
    const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
    expect(results.violations.filter((violation) => ['serious', 'critical'].includes(violation.impact ?? ''))).toEqual([])
  })
}
