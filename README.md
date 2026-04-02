# Triovexa

Triovexa is a Go-based AI incident triage operator. It receives alerts, gathers evidence, retrieves runbooks and postmortems, proposes constrained remediation actions, evaluates policy, executes approved actions, verifies outcomes, and rolls back when needed.

The codebase is designed around safety, auditability, and local demoability. The current implementation supports both lightweight local workflows and real-provider integrations through runtime-selectable adapters.

## What Is Included

- Grafana-compatible webhook intake
- incident list and incident detail UI
- evidence collection from either the demo service or Grafana datasource proxies
- runbook and postmortem retrieval from `docs/`
- heuristic triage and action generation
- Mistral-backed triage and action generation
- policy evaluation with allowlists, risk levels, and approval gates
- global kill switch
- runtime UI switches for `heuristic|mistral` and `demo|grafana`
- low-risk execution with timeout, retry, cooldown, and idempotency
- verification based on before/after signal comparison
- automatic rollback for selected medium-risk demo actions
- PostgreSQL-backed audit trail and persistence
- internal telemetry and diagnostics endpoints: `/metrics`, `/debug/tools`, `/debug/policies`

## Architecture At A Glance

```text
Grafana Alert
  -> Alert Receiver
  -> Incident Orchestrator
     -> Context Collectors
     -> Knowledge Retriever
     -> AI / Heuristic Layer
     -> Policy Engine
     -> Approval Layer
     -> Execution Engine
     -> Verification Engine
     -> Rollback / Escalation
  -> PostgreSQL Persistence + Audit Store
  -> Operator UI + Metrics + Diagnostics
```

The domain layer stays vendor-neutral. Observability, reasoning, execution, and verification integrations live behind adapters so the orchestration flow remains understandable and replaceable.

## Running Locally

1. Start local PostgreSQL:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev-up.ps1
```

2. Start the full local stack:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev-start.ps1
```

3. Open the main endpoints:

- UI: `http://localhost:8080/ui/incidents`
- Health: `http://localhost:8080/health`
- Metrics: `http://localhost:8080/metrics`
- Debug tools: `http://localhost:8080/debug/tools`
- Debug policies: `http://localhost:8080/debug/policies`
- Demo service state: `http://localhost:8090/state`

Example configuration lives in [config/app.example.env](config/app.example.env).

For quick demos without a ready PostgreSQL instance, you can start the server with `DATABASE_URL=memory`. That mode is intentionally ephemeral and only persists data for the lifetime of the process.

## Demo Scenarios

Use the helper script below to trigger end-to-end demo scenarios:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\demo-scenario.ps1 -Scenario timeout-after-deploy
powershell -ExecutionPolicy Bypass -File .\scripts\demo-scenario.ps1 -Scenario worker-stall
powershell -ExecutionPolicy Bypass -File .\scripts\demo-scenario.ps1 -Scenario error-rate-spike
```

Available scenarios:

- [docs/scenarios/timeout-after-deploy.md](docs/scenarios/timeout-after-deploy.md)
- [docs/scenarios/worker-stall-rollback.md](docs/scenarios/worker-stall-rollback.md)
- [docs/scenarios/error-rate-spike.md](docs/scenarios/error-rate-spike.md)

The full walkthrough is documented in [docs/operations/demo-guide.md](docs/operations/demo-guide.md).

## Testing

Main commands:

```powershell
go test ./...
go vet ./...
```

The test approach is documented in [docs/operations/testing-strategy.md](docs/operations/testing-strategy.md).

Current coverage includes:

- webhook intake
- triage and action generation flow
- policy decision
- approval workflow
- execution path
- verification path
- blocked action
- kill switch
- rollback path
- metrics and diagnostics endpoints
- runtime mode switching endpoint and UI controls

## Environment Variables

Currently used:

- `DATABASE_URL`
- `DOCS_ROOT`
- `DEMO_SERVICE_BASE_URL`
- `HTTP_PORT`
- `LOG_LEVEL`
- `KILL_SWITCH_ENABLED`
- `ACTION_EXECUTION_TIMEOUT`
- `ACTION_EXECUTION_COOLDOWN`
- `ACTION_EXECUTION_RETRIES`

Required when real integrations are enabled:

- `GRAFANA_BASE_URL`
- `GRAFANA_API_TOKEN`
- `GRAFANA_WEBHOOK_SECRET`
- `GRAFANA_METRICS_DATASOURCE_UID`
- `GRAFANA_LOGS_DATASOURCE_UID`
- `GRAFANA_ERROR_RATE_QUERY`
- `GRAFANA_LATENCY_QUERY`
- `GRAFANA_QUEUE_QUERY`
- `GRAFANA_REPLICA_QUERY`
- `GRAFANA_LOGS_QUERY`
- `GRAFANA_DEPLOY_LOGS_QUERY`
- `MISTRAL_API_KEY`
- `MISTRAL_MODEL`

## Safety Scope

- only actions present in the catalog can be evaluated
- high-risk actions remain blocked
- medium-risk actions stay approval-gated
- execution revalidates environment and target before the adapter is called
- verification distinguishes `success`, `failed`, and `inconclusive`
- automatic rollback only runs for actions that explicitly define a rollback plan

## Current Limitations

- `grafana` mode requires query templates that match your telemetry schema; the repository defaults cannot infer metric names or labels universally
- datasource discovery may succeed even when generic queries return no data because metrics or logs have not been ingested yet
- authentication for approval and execution endpoints is not implemented yet
- distributed tracing is not enabled yet; current observability covers structured logging and internal metrics

## License

This project is open sourced under the MIT License. See [LICENSE](LICENSE) for details.

## Additional Documentation

- [docs/architecture/high-level.md](docs/architecture/high-level.md)
- [docs/architecture/incident-state-machine.md](docs/architecture/incident-state-machine.md)
- [docs/architecture/workflow-engine-evaluation.md](docs/architecture/workflow-engine-evaluation.md)
- [docs/operations/demo-environment.md](docs/operations/demo-environment.md)
- [docs/operations/action-catalog-v1.md](docs/operations/action-catalog-v1.md)
- [docs/operations/policy-rules-v1.md](docs/operations/policy-rules-v1.md)
