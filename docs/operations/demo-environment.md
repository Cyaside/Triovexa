# Demo Environment

The demo service exists to generate incident signals that can be used by the intake, triage, remediation, verification, and rollback flows.

## Component

- `cmd/demo-service`
  A small HTTP service that can switch between healthy and incident conditions.

## Endpoints

- `GET /health`
- `GET /state`
- `GET /metrics`
- `POST /simulate/error-rate-spike`
- `POST /simulate/worker-stall`
- `POST /simulate/timeout-after-deploy`
- `POST /simulate/reset`
- `POST /actions/restart-worker`
- `POST /actions/retry-job`
- `POST /actions/refresh-cache`
- `POST /actions/pause-queue-consumer`
- `POST /actions/resume-queue-consumer`

## Incident Modes

- `healthy`
- `error_rate_spike`
- `worker_stall`
- `timeout_after_deploy`

## Example Startup

```powershell
go run ./cmd/demo-service
go run ./cmd/server
```

Before starting the main server, make sure `DATABASE_URL` points at a running PostgreSQL instance, or explicitly use `DATABASE_URL=memory` for an ephemeral local demo.

Fastest database bootstrap:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev-up.ps1
```

Most convenient way to start everything:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev-start.ps1
```

If you only want to start the application processes without bootstrapping the database again:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev-start.ps1 -SkipDatabase
```

If the script reports that the Docker daemon is unreachable, start Docker Desktop and try again.

## Example Incident Triggers

Fastest option:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\demo-scenario.ps1 -Scenario timeout-after-deploy
powershell -ExecutionPolicy Bypass -File .\scripts\demo-scenario.ps1 -Scenario worker-stall
powershell -ExecutionPolicy Bypass -File .\scripts\demo-scenario.ps1 -Scenario error-rate-spike
```

Manual option:

```powershell
Invoke-WebRequest -Method Post http://localhost:8090/simulate/error-rate-spike
Invoke-WebRequest -Method Post http://localhost:8090/simulate/worker-stall
Invoke-WebRequest -Method Post http://localhost:8090/simulate/reset
```

## Practical Purpose

- provide a target for Grafana alert rules
- provide simple metrics for observability
- provide reproducible conditions for triage and candidate-action demos

## UI And API Endpoints

- `GET /ui/incidents`
- `GET /ui/incidents/{id}`
- `GET /metrics`
- `GET /debug/policies`
- `POST /webhooks/grafana`
- `GET /incidents`
- `GET /incidents/{id}`
- `GET /incidents/{id}/triage`
- `GET /incidents/{id}/actions`
- `GET /actions/{id}/verification`
- `GET /actions/{id}/rollbacks`
- `POST /actions/{id}/approve`
- `POST /actions/{id}/reject`
- `POST /actions/{id}/execute`
- `POST /admin/kill-switch`

## Environment Variables

Used today:

- `DATABASE_URL`
- `DOCS_ROOT`
- `DEMO_SERVICE_BASE_URL`
- `HTTP_PORT`
- `LOG_LEVEL`
- `KILL_SWITCH_ENABLED`
- `ACTION_EXECUTION_TIMEOUT`
- `ACTION_EXECUTION_COOLDOWN`
- `ACTION_EXECUTION_RETRIES`

Needed when real integrations are enabled:

- `GRAFANA_BASE_URL`
  Used to retrieve additional context from the Grafana API instead of only receiving webhooks.
- `GRAFANA_API_TOKEN`
  Access token for datasource proxy queries and supporting Grafana APIs.
- `GRAFANA_WEBHOOK_SECRET`
  Secret used to verify Grafana webhooks.
- `GRAFANA_METRICS_DATASOURCE_UID`
  Metrics datasource UID used by the Grafana collector.
- `GRAFANA_LOGS_DATASOURCE_UID`
  Logs datasource UID used by the Grafana collector.
- `GRAFANA_ERROR_RATE_QUERY`
  Query template for the primary error-rate signal.
- `GRAFANA_LATENCY_QUERY`
  Query template for latency evidence.
- `GRAFANA_QUEUE_QUERY`
  Query template for queue backlog evidence.
- `GRAFANA_REPLICA_QUERY`
  Query template for replica-count evidence.
- `GRAFANA_LOGS_QUERY`
  Loki query template for general incident logs.
- `GRAFANA_DEPLOY_LOGS_QUERY`
  Loki query template for deployment-related logs.
- `MISTRAL_API_KEY`
  Credentials for the Mistral reasoning provider.
- `MISTRAL_MODEL`
  Model name used for triage and candidate action generation.

When these values are not configured, the application can still run in local demo mode using heuristic reasoning and the built-in demo collector.

## Example Approval, Execution, And Kill Switch Requests

Approve an action:

```powershell
Invoke-RestMethod -Method Post `
  -Uri http://localhost:8080/actions/<ACTION_ID>/approve `
  -ContentType "application/json" `
  -Body '{"approved_by":"operator-a","note":"safe to proceed"}'
```

Reject an action:

```powershell
Invoke-RestMethod -Method Post `
  -Uri http://localhost:8080/actions/<ACTION_ID>/reject `
  -ContentType "application/json" `
  -Body '{"approved_by":"operator-a","note":"needs manual investigation"}'
```

Execute an approved low-risk action:

```powershell
Invoke-RestMethod -Method Post `
  -Uri http://localhost:8080/actions/<ACTION_ID>/execute `
  -ContentType "application/json" `
  -Body '{"initiated_by":"operator-a","note":"execute approved action"}'
```

Execute an approved medium-risk action:

```powershell
Invoke-RestMethod -Method Post `
  -Uri http://localhost:8080/actions/<ACTION_ID>/execute `
  -ContentType "application/json" `
  -Body '{"initiated_by":"operator-b","note":"execute medium-risk action with rollback plan"}'
```

View the verification result for that action:

```powershell
Invoke-RestMethod -Method Get `
  -Uri http://localhost:8080/actions/<ACTION_ID>/verification
```

View rollback records for the same action:

```powershell
Invoke-RestMethod -Method Get `
  -Uri http://localhost:8080/actions/<ACTION_ID>/rollbacks
```

Enable the kill switch:

```powershell
Invoke-RestMethod -Method Post `
  -Uri http://localhost:8080/admin/kill-switch `
  -ContentType "application/json" `
  -Body '{"enabled":true}'
```

## Active Closed-Loop Verification

After `POST /actions/{id}/execute` succeeds, the main server automatically:

- captures the service state after the action
- compares before and after signals
- stores a `verification_result`
- moves the incident to `resolved` when the evidence clearly improved
- moves the incident to `escalated` when the result failed or stayed inconclusive
- triggers automatic rollback for medium-risk actions that define a rollback plan when verification fails
- moves the incident to `rolled_back` if rollback succeeds

The current rules remain simple and auditable:

- the alert is considered cleared when the demo mode returns to `healthy`
- the health check is considered normal when `worker_healthy=true`
- improvement is evaluated using `error_rate`, `latency_ms`, and `queue_backlog`
- mixed signals do not auto-resolve; they escalate instead
