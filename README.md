# Triovexa

Triovexa is an approval-gated incident response system for stalled workers and queue backlogs. It receives Prometheus alerts through Alertmanager, gathers workload evidence, proposes an allowlisted remediation, records an operator approval, executes the action through a constrained supervisor API, and verifies recovery from telemetry.

The repository includes a real local workload: a producer and Redis Streams worker, PostgreSQL persistence, Prometheus, Alertmanager, Grafana, Loki with Grafana Alloy log collection, and a React operator console. The reasoning layer works without an API key through deterministic heuristics and can use Mistral or another OpenAI-compatible Chat Completions endpoint.

## Proven path

The automated Compose scenario validates this sequence:

```text
worker stall
  -> Redis backlog grows
  -> Prometheus rule fires
  -> Alertmanager sends a webhook
  -> Triovexa creates and triages the incident
  -> operator approval is recorded
  -> the supervisor restarts the worker
  -> backlog falls
  -> three consecutive observations confirm recovery
```

The latest checked-in evidence and its limits are described in [docs/evidence/README.md](docs/evidence/README.md). The runner uses only project-scoped Compose resources and refuses to start when port 8080 is occupied by another process.

## Run the complete demo

Requirements: Docker Desktop, Docker Compose, and PowerShell 7 or Windows PowerShell 5.1.

```powershell
./scripts/demo.ps1 -Reset
```

The command builds the images, starts the stack, injects the bounded fault, completes the approval and execution flow, verifies recovery, and writes machine-readable evidence under `artifacts/e2e/`. It leaves the stack running for inspection:

- Operator console: <http://127.0.0.1:8080/ui/incidents>
- Grafana: <http://127.0.0.1:3300>
- Readiness: <http://127.0.0.1:8080/health>

Stop only this project with:

```powershell
docker compose -p triovexa-e2e down
```

## Operator console

The React + TypeScript console is served as embedded static assets by the Go server. It provides:

- server-filtered incident search, filters, and pagination;
- incident evidence, actions, verification, and audit history;
- approval and execution controls with explicit conflict handling;
- Grafana, Prometheus, Alertmanager, Loki, and reasoning-provider status;
- Grafana onboarding and query preview;
- Mistral and OpenAI-compatible provider configuration by credential reference;
- bounded playground controls for worker stall and processing failures;
- loading, empty, stale, disconnected, forbidden, and `409` conflict states.

The UI has no separate production frontend server. Build it with:

```powershell
cd ui
npm ci
npm run build
```

## Reasoning providers

Reasoning mode and provider are separate settings:

- `heuristic` runs without external credentials;
- `llm` uses either `mistral` or `openai-compatible`;
- every generated action is validated against the same action catalog and policy boundaries;
- provider timeout, malformed output, or missing configuration falls back to heuristics and is recorded.

The OpenAI-compatible adapter uses the Chat Completions contract. Configure an API root, model, credential environment-variable name, timeout, and JSON-mode capability in **Connections**. HTTP endpoints are accepted only on loopback in local mode; internal deployments require HTTPS and an administrator allowlist.

## Safety and durability

- PostgreSQL migrations are versioned in `schema_migrations`.
- Webhook intake stores the incident, audit event, and triage job in one transaction.
- Workflow jobs use leases, bounded retries, conditional transitions, and startup recovery.
- Execution claims and approval transitions are atomic, idempotent, and scoped to an allowlisted target.
- Browser mutations require an authenticated session, CSRF protection, and a valid origin in internal mode.
- Webhooks have a separate credential, request-size bound, and rate limit.
- Approvals expire and bind action parameters, target, evidence, and policy version.
- The persisted kill switch blocks new approvals and executions.
- Triovexa never receives a Docker socket and cannot run arbitrary commands.

## Evaluation

Run the fixed 30-case heuristic baseline:

```powershell
go run ./cmd/evaluator
```

Run a provider three times per case:

```powershell
$env:TRIOVEXA_EVAL_API_KEY = "..."
go run ./cmd/evaluator -mode provider -provider openai-compatible `
  -base-url https://provider.example/v1 -model model-name -repeats 3 `
  -out evaluation/results/provider-model.json
```

Reports include accuracy, evidence grounding, invalid proposals, fallbacks, latency, and token usage when the endpoint returns it. The committed baseline is in [evaluation/results/heuristic-baseline.md](evaluation/results/heuristic-baseline.md).

## Verification

```powershell
go test ./...
go vet ./...
cd ui
npm ci
npm run build
npm run test:e2e
```

CI also runs the Go race detector, real PostgreSQL and Redis integration tests, migration re-entry, and PostgreSQL backup/restore verification.

## Documentation

- [Final architecture](docs/architecture/final-architecture.md)
- [Operational documentation](docs/README.md)
- [Release notes and limitations](docs/releases/v1.0.0-rc1.md)

## Current limits

Triovexa is a release candidate for local and small internal staging use. It is single-tenant and supports one bounded Redis workload integration. It does not claim general autonomous production remediation or compatibility with every API that describes itself as OpenAI-compatible.

