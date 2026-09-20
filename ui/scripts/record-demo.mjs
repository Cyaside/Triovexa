import { chromium } from '@playwright/test'
import { mkdir, rename, rm } from 'node:fs/promises'
import path from 'node:path'

const baseURL = process.env.TRIOVEXA_DEMO_URL || 'http://127.0.0.1:8080'
const outputDir = path.resolve(process.env.TRIOVEXA_DEMO_OUTPUT || '../artifacts/demo')
await mkdir(outputDir, { recursive: true })

const browser = await chromium.launch({ headless: true })
const context = await browser.newContext({
  viewport: { width: 1440, height: 900 },
  recordVideo: { dir: outputDir, size: { width: 1440, height: 900 } },
  colorScheme: 'dark',
})
const page = await context.newPage()

async function caption(title, text, seconds) {
  await page.evaluate(({ title, text }) => {
    document.querySelector('#demo-caption')?.remove()
    const element = document.createElement('aside')
    element.id = 'demo-caption'
    element.setAttribute('aria-label', 'Demo narration caption')
    element.innerHTML = `<strong>${title}</strong><span>${text}</span>`
    Object.assign(element.style, {
      position: 'fixed', left: '50%', bottom: '28px', transform: 'translateX(-50%)',
      zIndex: '99999', width: 'min(820px, calc(100vw - 48px))', padding: '16px 20px',
      background: '#090909', color: '#f4f4f5', border: '1px solid #343434', borderRadius: '8px',
      boxShadow: '0 16px 48px rgba(0,0,0,.55)', font: '14px/1.45 Inter, system-ui, sans-serif',
      display: 'grid', gap: '5px'
    })
    element.querySelector('strong').style.fontSize = '16px'
    element.querySelector('span').style.color = '#b8b8bd'
    document.body.appendChild(element)
  }, { title, text })
  await page.waitForTimeout(seconds * 1000)
}

try {
  await page.goto(`${baseURL}/ui/incidents`, { waitUntil: 'networkidle' })
  await caption('Triovexa', 'Approval-gated incident response for worker stalls and queue backlogs.', 10)
  await caption('Incident queue', 'Search, filters, severity, target, state, and timestamps stay visible in one dense operator view.', 14)

  const incidentLink = page.locator('a[href^="/ui/incidents/"]').first()
  if (await incidentLink.count()) {
    await incidentLink.click()
    await page.waitForLoadState('networkidle')
  }
  await caption('Incident overview', 'The summary connects the firing alert to collected evidence and the current recovery state.', 18)

  const evidenceTab = page.getByRole('tab', { name: /Evidence/ })
  if (await evidenceTab.count()) await evidenceTab.click()
  await caption('Evidence', 'The operator can inspect the workload snapshot, source, timestamp, and completeness without opening server logs.', 18)

  const actionsTab = page.getByRole('tab', { name: /Actions/ })
  if (await actionsTab.count()) await actionsTab.click()
  await caption('Bounded action', 'Restart targets an allowlisted worker. Approval binds the action, parameters, evidence, and policy version.', 18)
  await caption('Verified recovery', 'Execution success and recovery are separate. Three consecutive healthy observations are required.', 15)

  const activityTab = page.getByRole('tab', { name: /Activity/ })
  if (await activityTab.count()) await activityTab.click()
  await caption('Audit trail', 'Intake, triage, policy, approval, execution, reconciliation, and verification remain inspectable.', 14)

  await page.goto(`${baseURL}/ui/connections`, { waitUntil: 'networkidle' })
  await caption('Connections', 'Grafana, Prometheus, Alertmanager, Loki, and the reasoning provider expose explicit health and configuration.', 18)

  await page.goto(`${baseURL}/ui/playground`, { waitUntil: 'networkidle' })
  await caption('Real playground', 'Fault controls call a bounded supervisor API while Redis Streams continues to receive production-like jobs.', 15)

  await page.goto(`${baseURL}/ui/settings`, { waitUntil: 'networkidle' })
  await caption('Safety controls', 'Operators can choose heuristic or LLM reasoning and persistently stop new actions with the kill switch.', 12)
  await caption('Evidence over claims', 'The automated Compose run exports all seven gates and the individual recovery observations as JSON.', 8)
} finally {
  const video = page.video()
  await page.close()
  await context.close()
  await browser.close()
  if (video) {
    const source = await video.path()
    const destination = path.join(outputDir, 'triovexa-demo.webm')
    if (source !== destination) {
      await rm(destination, { force: true })
      await rename(source, destination)
    }
    console.log(destination)
  }
}
