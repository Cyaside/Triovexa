import { useState } from 'react'

const list = (value: FormDataEntryValue | null) => String(value || '').split(',').map((v) => v.trim()).filter(Boolean)

export function telemetryProfile(data: FormData) {
  const kind = String(data.get('telemetry_kind'))
  if (!['http', 'queue'].includes(kind)) throw new Error('Choose a telemetry profile.')
  const common = { version: 'telemetry-v1', kind, alert_name: String(data.get('alert_name')).trim(), log_sources: list(data.get('log_sources')) }
  if (kind === 'queue') return common
  const labels: unknown = JSON.parse(String(data.get('request_labels') || '{}'))
  if (!labels || typeof labels !== 'object' || Array.isArray(labels) || !Object.values(labels).every((v) => typeof v === 'string')) throw new Error('Request labels must be a JSON object of literal string values.')
  return { ...common, http: {
    requests_metric: String(data.get('requests_metric')).trim(), health_metric: String(data.get('health_metric')).trim(), revision_metric: String(data.get('revision_metric')).trim(),
    revision_label: String(data.get('revision_label')).trim(), status_label: String(data.get('status_label')).trim(), service_label: String(data.get('service_label')).trim(),
    environment_label: String(data.get('environment_label')).trim(), request_labels: labels,
    window_seconds: Number(data.get('window_seconds')), minimum_requests: Number(data.get('minimum_requests')), max_error_ratio: Number(data.get('max_error_ratio')),
  } }
}

export function TelemetrySetup() {
  const [kind, setKind] = useState('')
  return <fieldset><legend>Evidence and recovery</legend>
    <label>Telemetry profile<select name="telemetry_kind" required value={kind} onChange={(e) => setKind(e.target.value)}>
      <option value="">Choose a service profile</option><option value="http">HTTP service</option><option value="queue">Redis queue worker</option>
    </select></label>
    <p className="muted">The same profile collects trusted revision evidence and verifies recovery after deployment. Missing or stale telemetry cannot prove recovery.</p>
    <label>Prometheus alert name<input name="alert_name" placeholder="HTTPServerErrors" required /></label>
    <label>Trusted log sources, comma separated<input name="log_sources" defaultValue="grafana-loki" required /></label>
    {kind === 'http' && <>
      <p className="muted">Metrics must identify this service and environment. The revision metric must expose the actual deployed Git SHA. Configure metric names, not PromQL expressions.</p>
      <label>HTTP request counter<input name="requests_metric" placeholder="http_requests_total" required /></label>
      <label>Health gauge<input name="health_metric" placeholder="probe_success" required /><span className="muted">One means healthy; zero means unhealthy. All matching instances must be healthy.</span></label>
      <label>Revision info metric<input name="revision_metric" placeholder="app_build_info" required /></label>
      <label>Revision label<input name="revision_label" defaultValue="revision" required /></label>
      <label>HTTP status label<input name="status_label" defaultValue="status" required /></label>
      <label>Service label<input name="service_label" defaultValue="service" required /></label>
      <label>Environment label<input name="environment_label" defaultValue="environment" required /></label>
      <label>Request labels (JSON)<textarea name="request_labels" defaultValue="{}" rows={2} /><span className="muted">Optional route labels, for example {JSON.stringify({ path: '/healthz' })}. Identity labels cannot be overridden.</span></label>
      <label>Observation window, seconds<input name="window_seconds" type="number" min={10} max={60} defaultValue={30} required /></label>
      <label>Minimum requests per window<input name="minimum_requests" type="number" min={1} defaultValue={1} required /></label>
      <label>Maximum HTTP 5xx ratio<input name="max_error_ratio" type="number" min={0} max={0.99} step={0.001} defaultValue={0.01} required /></label>
    </>}
  </fieldset>
}
