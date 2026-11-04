# Evidence and recovery profiles

A repository binding selects the telemetry contract for its service and environment. The same contract governs investigation evidence, the deployment baseline and post-deployment recovery. The agent cannot select a metric, change the contract or supply a deployed revision through alert text.

Register a profile in **Settings → Source repositories → Evidence and recovery**. New bindings require a `telemetry-v1` profile. Profile changes require a replacement binding; they invalidate the previous investigation and publication scope. Deployment stores its own contract snapshot, and the verifier rejects a disabled or changed binding.

Build the API, investigation runtime, verifier and sandbox runner from the same release. Rebuild prepared language images when adopting telemetry profiles so their bundled runner understands the new binding contract. Historical bindings omit absent telemetry fields from sandbox frames and retain their existing queue behavior.

## HTTP services

The HTTP profile covers health and HTTP 5xx regressions. Configure these signals in the server's Prometheus connection:

- A monotonically increasing request counter with service, environment and three-digit HTTP status labels. Optional literal request labels can restrict the check to a route.
- A health gauge: `1` for healthy, `0` for unhealthy. Every matching instance must be healthy. Use a signal that represents the target health check; scrape availability alone does not establish application health.
- A revision info metric with value `1` and a label containing the full deployed Git SHA. All matching instances must agree on the revision.
- A uniquely named, healthy alert rule, evaluated within the last minute. Its alert instances must identify the same service and environment as the binding.
- Fresh logs from a trusted configured collector. The supplied Grafana/Loki collector identifies these as `grafana-loki`.

Health and revision vectors must cover the same `instance` labels. Multiple series without instance identity, duplicate instances and missing revision observations are rejected. A single health/revision pair without an instance label represents one authoritative service-level observation.

For example, send this `telemetry_profile` with repository registration:

```json
{
  "version": "telemetry-v1",
  "kind": "http",
  "alert_name": "APIHTTPServerErrors",
  "log_sources": ["grafana-loki"],
  "http": {
    "requests_metric": "http_requests_total",
    "health_metric": "probe_success",
    "revision_metric": "app_build_info",
    "revision_label": "revision",
    "status_label": "status",
    "service_label": "service",
    "environment_label": "environment",
    "request_labels": {"path": "/healthz"},
    "window_seconds": 30,
    "minimum_requests": 1,
    "max_error_ratio": 0.01
  }
}
```

The metric identifiers and label names must match the actual instrumentation. Request labels apply only to the request counter; service and environment labels apply to every query. Names accept identifiers, not PromQL expressions. Values are escaped as literal label values, and request labels cannot replace identity, revision or status labels.

Triovexa renders bounded queries server-side. Request counts use `increase` over the configured 10–60 second window, grouped by service, environment and HTTP status. Counter resets are handled by Prometheus. Empty results, invalid status labels, non-finite values, query warnings and conflicting revisions stop observation explicitly.

Queries filter original sample age using [`timestamp` and `time`](https://prometheus.io/docs/prometheus/latest/querying/functions/#timestamp). The query evaluation time alone is insufficient evidence that the underlying sample is fresh. Samples older than 60 seconds or more than five seconds in the future are excluded. Tests evaluate these expressions with a real Prometheus engine, including an old gauge that remains inside Prometheus's normal lookback window.

Investigation collects profile-matched metrics from Prometheus and logs from the configured Grafana collector using a one-minute log window. It does not query the local queue supervisor or unrelated metric queries for HTTP bindings. A fresh, complete profile-matched observation supplies deployed revision provenance; alert payloads cannot supply or override it. Approval checks the stored metric source, service, environment, profile digest and revision again inside its transaction.

## Queue workers

The queue adapter reads authenticated supervisor state and the configured Prometheus alert:

```json
{
  "version": "telemetry-v1",
  "kind": "queue",
  "alert_name": "TriovexaQueueBacklogHigh",
  "log_sources": ["grafana-loki"]
}
```

The supervisor's observed target and environment must match the binding. Set `WORKLOAD_ENVIRONMENT` on the supervisor (the supplied Compose stack uses `local`) and configure its endpoint and credential through `WORKLOAD_CONTROL_BASE_URL` and `WORKLOAD_CONTROL_TOKEN`; HTTP profiles do not require these. New queue profiles require alert labels `service` and `environment` to match the binding. Queue recovery requires healthy processing, increasing processed-job count, a falling or empty backlog and no increase in errors.

Bindings saved before profile support retain their historical queue contract. Migration `014_repair_telemetry_profiles` adds nullable profile and deployment-contract columns without rewriting existing case digests or evidence. A missing profile on an old binding does not select an HTTP adapter. Existing queue records remain readable; new registration requires an explicit supported profile.

## Deployment and recovery

The pipeline sends authenticated `started` and `completed` events to `/webhooks/deployment/repair` for the same case, environment, deployment ID and exact merged revision. Send `started` before rollout so Triovexa can capture a fresh baseline. Send `completed` only when the rollout is complete and the revision metric represents the deployed artifact. The pipeline remains responsible for deployment; Triovexa does not merge or deploy a pull request.

The verifier reloads the persisted contract and observes it for at most two minutes. Three consecutive passing observations must be at least ten seconds apart. It spaces requests after sampling completes, and storage independently checks spacing, timestamps, revision and criteria before accepting `recovered`.

For HTTP recovery, the baseline must show an active target alert and an unhealthy service or excessive 5xx ratio, with sufficient request volume. Each passing observation requires healthy instances, the exact merged revision, enough requests in the configured window, a 5xx ratio within the configured threshold and the target alert cleared. Other services' alert instances do not determine this service's recovery. No traffic is not proof of recovery.

Missing or stale telemetry, mixed revisions, a failed health check or unproven recovery results in `inconclusive`. A deployment event or passing source tests alone cannot establish recovery. The operator console shows the pinned profile, thresholds and baseline alongside the deployment result. Collecting evidence and checking recovery make no model requests.

Latency-only, database-specific and other recovery semantics need a suitable additional profile. Do not repurpose a health/5xx profile to claim those incidents recovered. Repository investigation remains language-neutral and subject to its configured scope and test recipes.
