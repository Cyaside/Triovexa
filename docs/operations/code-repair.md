# Code repair (staging)

Triovexa can investigate an incident against its registered GitHub repository and prepare a bounded source patch. Investigation starts with operator approval or an administrator's expiring automatic-investigation grant. The operator reviews the resulting diff and red-to-green regression test, then separately authorizes publication of a **draft** GitHub pull request. GitHub CI and human review still control merge. The deployment pipeline remains responsible for rollout; Triovexa only marks the repair recovered after the deployed merge revision and three workload observations agree.

Source validation is language-neutral. Each service/environment binding pins its repository, allowed paths, sandbox image, formatter/linter checks, and regression command. An offline Python fixture verifies this boundary with real Docker tests; the deployed recovery integration still targets the bounded `queue-worker` staging workload. Each additional workload needs a trusted deployed-revision signal and verified recovery adapter. The agent cannot choose a repository, command, image, or credential, change protected tests or CI files, auto-merge, or deploy code.

## Register a repository

An administrator opens **Settings → Source repositories** and registers the service and environment used by incoming alerts, the canonical GitHub HTTPS URL, base branch, and allowed repository paths. Include any configuration or test files the agent needs to read in those paths; the validation profile's root files and protected test paths remain immutable. Configure a prebuilt image containing `triovexa-repair-sandbox` and the repository's language tools and dependencies. Prefer an image digest when deploying. Tests run as UID/GID 10001 in a container with no network, a read-only checkout, and bounded resources. Dependency installation belongs in image preparation, before investigation.

For example, build a Python runtime:

```sh
docker build -f Dockerfile.repair-sandbox \
  --build-arg RUNTIME_IMAGE=python:3.12-alpine \
  -t triovexa-repair-sandbox:python .
```

This image can run standard-library tests. Projects that require packages need their own prepared base image. A validation profile for a protected `tests/` suite might use:

```json
{
  "id": "repository-tests",
  "version": "validation-v1",
  "image": "triovexa-repair-sandbox:python",
  "root_files": ["pyproject.toml"],
  "protected_paths": ["tests"],
  "checks": [],
  "test": {
    "executable": "/usr/local/bin/python",
    "arguments": ["-m", "unittest", "discover", "-s", "tests", "-v"],
    "timeout_seconds": 120
  },
  "expected_test_name": "test_accepts_one",
  "expected_failure": "one rejected"
}
```

The failing baseline must contain both configured regression markers. A successful patch must pass all configured checks and tests without timeouts or truncated output. Formatter commands must check formatting without writing to the checkout; use `require_empty_output: true` for tools such as `gofmt -l`. Shell commands and inline programs are rejected. Existing Go fixture bindings retain their explicitly registered recipes; a new binding requires a validation profile. Changing configuration requires disabling the old binding and registering a replacement, which invalidates the old scope.

Public repositories need no checkout credential. Private repositories use a server-side read credential reference, such as `env:REPAIR_GITHUB_READ_TOKEN` or `file:<absolute-secret-file>`. Provision it on both the API server and the host investigation worker, then use **Check repository access** to verify the configured branch. The token is passed to Git through ephemeral environment configuration, never stored in a repository URL, Git config, model context, or test container. The separate publisher credential below remains responsible for writes and pull requests.

## Investigate incoming alerts automatically

The binding's **Automatically investigate incoming alerts** setting authorizes investigation only. An administrator specifies an existing model campaign ID, grant expiry, maximum investigations, and maximum model requests per investigation. The campaign must match the server's sealed reasoning/budget configuration; its global spend and request limits still apply. The `final-smoke` validation campaign cannot be used for automatic alerts.

After alert intake and fresh evidence collection, a matching enabled grant dispatches investigation directly. Restart or failed remediation is not a prerequisite. Case, evidence, authorization, attempt, durable job, and alert deduplication commit in one PostgreSQL transaction. Duplicate deliveries and runner restarts cannot allocate a second investigation for that incident/binding. A new incident episode consumes another slot from the same grant. Missing evidence, exhausted budget, or invalid authorization stops this route explicitly, without falling back to a restart or another model.

The separate **Publish a draft PR after the patch passes validation** permission enables automatic publication for investigations created under that same grant. It defaults to off. One successful investigation can produce one draft PR; the patch digest, red-to-green test proof, original administrator, binding scope and grant expiry are checked before a durable publication is queued. Job completion and publication authorization commit together, including recovery after an outcome was saved before a process crash. Revoked or expired grants retain the verified patch for operator review without authorizing a GitHub write. The dedicated publisher rechecks permission before writing and reconciles existing branches and PRs after uncertain responses. This grant never authorizes merge or deployment.

Disabling the binding, expiry, removal of its administrator role, or the kill switch blocks further work. The worker rechecks authorization before checkout and at model/tool boundaries. Automatic publication is not enabled by this grant: a verified patch still requires the separate review and publication flow.

## Prepare the investigation runtime

The host investigation worker requires the Go version specified in `go.mod`, Node.js 24, Git, and a running Docker daemon. The Node runtime runs the investigation; Go owns repository access, approved test recipes, budget accounting, and patch validation. Build the runtime from the repository root:

```powershell
Push-Location agent-runtime
npm ci --ignore-scripts
npm run typecheck
npm run build
Pop-Location
```

The pinned runtime currently includes the unresolved `braces` stack-exhaustion advisory [GHSA-vfj7-8cjw-p6xm](https://github.com/advisories/GHSA-vfj7-8cjw-p6xm) through its framework dependency chain. Default glob, grep, shell, and task tools are disabled; passing the offline tests does not establish that every dependency path is unreachable. Review the advisory and a compatible upstream fix before operational deployment. Downgrading the framework to clear the audit report is not a supported upgrade.

Initialize checkpoint storage before starting the worker. Use a dedicated PostgreSQL database and a dedicated runtime role. An administrator runs initialization and framework migrations; the runtime role receives schema `USAGE`, CRUD permissions on the three checkpoint tables, and read-only access to migration metadata. It cannot run migrations or access application tables.

Set the following variables through your deployment environment or secret manager:

| Variable | Initialization value |
| --- | --- |
| `TRIOVEXA_CHECKPOINT_ADMIN_DSN` | Connection string for an administrator of the dedicated checkpoint database. |
| `TRIOVEXA_CHECKPOINT_SCHEMA` | Checkpoint schema name, for example `triovexa_checkpoints`. |
| `TRIOVEXA_CHECKPOINT_ROLE` | Dedicated role name, for example `triovexa_repair`. |
| `TRIOVEXA_CHECKPOINT_ROLE_PASSWORD` | Password for that role. |

For a dedicated checkpoint database, enable database hardening and initialize the schema:

```powershell
$env:TRIOVEXA_CHECKPOINT_HARDEN_DEDICATED_DATABASE = 'true'
node agent-runtime/dist/src/checkpoints/init.js
```

This option revokes PostgreSQL's default temporary-table permission from `PUBLIC` in that database. Use it only for the dedicated database. If an administrator prepares permissions separately, omit the option and ensure the runtime role has no database `CREATE` or `TEMPORARY`, schema `CREATE`, elevated privileges, or role memberships. Initialization can be run again to apply checkpoint migrations. Normal investigations cannot run migrations.

Configure the host worker before launching it:

| Variable | Worker value |
| --- | --- |
| `REPAIR_AGENT_ENTRY` | Absolute path to `agent-runtime/dist/src/main.js`. |
| `REPAIR_CHECKPOINT_DATABASE_URL` | Connection string using the restricted runtime role, never the administrator role. |
| `REPAIR_CHECKPOINT_SCHEMA` | The schema initialized above. |
| `AI_BUDGET_CONFIG_PATH` | Path to the validated [campaign and pricing configuration](model-budget.md) shared with the Triovexa server. |

```powershell
$env:REPAIR_AGENT_ENTRY = (Resolve-Path agent-runtime/dist/src/main.js).Path
```

Keep database credentials and budget configuration outside version control. The runtime refuses incompatible checkpoint metadata or unsafe database privileges. Reuse the same checkpoint database and campaign configuration after a restart so accepted work and accounted requests remain available.

## Back up and retain repair state

Back up the application database and the checkpoint database together with their deployed runtime version, budget configuration, and credential-encryption key. The application database contains approval scope, accounting, and sealed tool/model receipts; checkpoint storage contains private transcripts, source excerpts, and framework state. Apply the same access controls to checkpoints and backups as to the source repository.

For the checkpoint database, use an administrator-managed `pg_dump --format=custom --no-owner --no-privileges` backup. A per-database dump does not include PostgreSQL roles. During restore, restore into a new dedicated database, run the administrative checkpoint initializer to create the restricted role and restore grants, and verify compatibility before directing workers to it. Keep the application and checkpoint restore points consistent; mismatched proof or immutable configuration blocks resume.

Before upgrading the agent runtime, back up both databases and retain the previous lockfile/build. Apply checkpoint migrations through the initializer, then run the offline checkpoint and recovery tests against the upgrade. Do not resume an attempt under a different pinned engine, prompt, or playbook version.

There is currently no automatic checkpoint-retention or purge job. Define a retention period in your deployment, retaining all active, uncertain, and recoverable attempts. Once a case is terminal and no recovery or audit requirement remains, an administrator can archive its records and remove the corresponding checkpoint thread from `checkpoints`, `checkpoint_blobs`, and `checkpoint_writes` together in a transaction. Keep accounting and approval/audit records according to your deployment policy; deleting a transcript must not reset a campaign budget. Never purge a thread while a worker holds its lease.

## Run the components

The normal Compose stack supplies PostgreSQL, the workload supervisor, and the UI. Configure an OpenAI-compatible reasoning connection in **Connections**, then register the repository in **Settings** or the incident's **Code repair** tab. Manual investigation remains available for escalated or failed-remediation incidents; a matching automatic grant can start directly from an alert. A proposal requires fresh incident evidence, including a trusted deployed revision and log signal.

The investigation worker uses Docker Desktop from the host so the sandbox receives a private checkout without exposing the Docker socket to the Triovexa server. After preparing the runtime and its environment above, run this in a dedicated terminal on Windows while the Compose stack is up:

Set `WORKLOAD_DEPLOYED_REVISION` to the actual 40-character Git commit currently running in the workload supervisor. Code repair remains ineligible when this signal is absent or does not match fresh incident evidence. When rolling out a merged repair, update the workload image and this revision together; the deployment event alone cannot change the reported revision.

```powershell
./scripts/run-repair-runner.ps1
```

The script builds the isolated sandbox image if needed and temporarily reads the existing credential key from the running Triovexa container. It deletes its temporary copy when the worker stops. It never creates a replacement key.

For draft PR publication and deployment verification, set the following server-side environment variables before starting the `code-repair` Compose profile:

| Variable | Used by |
| --- | --- |
| `REPAIR_GITHUB_TOKEN` | Dedicated publisher; fine-grained access to the registered repository's contents and pull requests. |
| `REPAIR_GITHUB_WEBHOOK_SECRET` | Triovexa server; validates GitHub `pull_request` webhook signatures. |
| `REPAIR_DEPLOYMENT_TOKEN` | Triovexa server and the existing deployment pipeline; authenticates rollout events. |

```powershell
docker compose --profile code-repair up -d repair-publisher repair-verifier
```

Configure a GitHub `pull_request` webhook to `POST /webhooks/github/repair` with the same secret. Keep the publisher token out of the investigation worker and browser. Private checkout uses the separate read credential reference configured on the binding.

The deployment pipeline sends two authenticated JSON requests to `POST /webhooks/deployment/repair` for the **merge commit SHA**, same case and deployment ID. Both the workload supervisor and Prometheus alert API must be reachable:

```json
{"case_id":"<case-id>","deployment_id":"<deployment-id>","environment":"staging","revision_sha":"<merge-sha>","phase":"started"}
```

Send `started` immediately before rollout so Triovexa captures the pre-deploy workload baseline. After the new revision is rolled out, send the same body with `"phase":"completed"`. The endpoint requires `Authorization: Bearer <REPAIR_DEPLOYMENT_TOKEN>`. The verifier then samples the supervisor and Prometheus every 10 seconds for up to two minutes. It requires the new revision, healthy worker, job progress, falling or empty backlog, no increase in errors, and the queue-backlog alert cleared in three consecutive samples. Missing or stale telemetry becomes `inconclusive`.

## Safety and recovery

Publication approval lasts 15 minutes and binds the case, repository scope, base commit, policy, attempt, and patch SHA-256. The publisher has its own credential and stable operation ID. After an uncertain GitHub response or process restart, it checks the dedicated branch and PR before writing again; a conflicting branch or PR blocks publication. Verified GitHub webhook deliveries are deduplicated and cannot authorize deployment.

The UI shows evidence, test proof, diff, approval, PR, deployment, and verification state. `patch_ready`, `pr_open`, and `merged` do **not** mean the incident recovered. The deployment pipeline and a reviewer remain separate decision makers. The deterministic CI gate uses a scripted model and fake GitHub API; a live provider and real GitHub staging run must be evaluated separately before relying on this workflow for operational use.

## Validate a configured provider

The optional provider smoke test runs one source-repair case in a temporary local Git repository. The model receives the failing baseline and a protected regression suite that requires both supported schema versions to keep working. It must propose the patch; the harness supplies no expected diff. Docker executes the registered recipe before and after the patch. This test validates a provider investigation, not GitHub publication, a deployment, or recovery of a running service.

Run the offline tests first. Build the Node runtime and sandbox image, and prepare a dedicated, administrator-owned loopback PostgreSQL test database whose name contains `test`. Initialization hardens checkpoint permissions and revokes database-wide default temporary-table access, so this database must not contain other workloads. The live harness verifies physical cluster/database identities and rejects reuse of the shared application database, including through a different hostname or credential. Both database credentials must permit read-only `pg_control_system()` metadata; unavailable metadata blocks the test. The harness creates uniquely named application and checkpoint schemas and retains them, together with its private fixture checkout, on success or failure. Its accounting uses the existing application database's campaign; it never creates a replacement campaign or reads provider credentials from environment variables.

Only enable this test after verifying the configured endpoint's tariff, conservative input bound, and limit on every billable output category. Use the `final-smoke` profile and the existing encrypted connection saved through **Connections**. Preflight checks the cumulative US$0.10 target, US$0.20 hard ceiling, prior dispatches, and uncertain reservations before opening the credential key. CI cannot run the test.

An optional `pricing.input_contract` pins a model-specific input bound in the immutable configuration. For `glm-5.3-flash`, `glm-5.3-flash-template-690b705-v1` counts the published chat template's UTF-8 bytes, including tool definitions, XML arguments, escaping, and retained reasoning. Its byte-level tokenizer makes this a conservative token bound rather than a tokens-per-character estimate. Confirm the endpoint uses that template and tokenizer before selecting it; model aliases and unsupported input shapes are rejected. Transport size and token admission are separate limits. `max_tokens` must also bound billed thinking and output for the configured endpoint.

For this named input contract, the native runtime first measures its final SDK payload through a private, authenticated local preview. This request performs no inference and creates no ledger reservation. Completed tool output can be replaced with immutable virtual-file references. Older, complete read exchanges can be archived with their original reasoning and results, while the initial scope, latest paired exchange, and patch/test proof stay in the model context. Original history remains available in checkpoints and virtual transcripts. The gateway measures the resulting payload again; if it still exceeds the limit, investigation stops before provider dispatch. A successful preview grants no spending permission: dispatch independently checks the current shared campaign and atomically reserves its verified allowance.

Validated response receipts settle reserved input to actual prompt usage. Pending or uncertain requests retain their full input allowances; replay cannot release an allowance twice. Original request bounds, money accounting, request counts, and campaign limits remain unchanged. Migration `010_ai_input_settlement` validates retained receipts before updating existing campaign counters. Stop all ledger writers for this upgrade and update every caller together; an older caller must not continue writing the previous input-accounting semantics. Back up the database first. Invalid receipts or inconsistent counters abort the migration without replacing history.

The one-case `final-smoke` writer exposes only `repo_read`, `read_file`, `propose_patch`, and `cannot_determine`. Its synthetic evidence identifies the source path without supplying a fix. Go runs the baseline and candidate recipes automatically. The ordinary internal profile retains source discovery and explicit test tools. Both process boundaries check the selected inventory, and its checkpoint identity prevents resuming an older validation with different tools.

Set these variables through a private local environment; do not commit their values:

| Variable | Value |
| --- | --- |
| `RUN_NATIVE_PROVIDER_SMOKE` | `one-approved-case` to explicitly enable one local case. |
| `AI_BUDGET_CONFIG_PATH` | Absolute path to the verified, unchanged campaign configuration. |
| `LIVE_EXPECTED_CAMPAIGN_ID` | Existing shared campaign ID, matching the configuration. |
| `LIVE_SHARED_DATABASE_URL` | Existing application database containing the campaign and saved encrypted connection. |
| `LIVE_CREDENTIAL_KEY_PATH` | Absolute path to its existing credential-encryption key; no replacement key is created. |
| `LIVE_PROVIDER_ALLOWED_HOST` | Exact approved HTTPS hostname of the configured provider; port must be standard HTTPS. |
| `TEST_DATABASE_URL` | Separate isolated loopback test database with administrative schema/role permissions. |
| `LIVE_ISOLATED_DATABASE_NAME` | Exact database name, confirming the dedicated test database selected above. |
| `TEST_AGENT_RUNTIME_ENTRY` | Absolute path to the built `agent-runtime/dist/src/main.js`. |
| `TEST_REPAIR_SANDBOX_IMAGE` | Existing sandbox image built from `Dockerfile.repair-sandbox`. |
| `LIVE_PROVIDER_EVIDENCE_PATH` | New absolute `.json` path under the ignored repository `artifacts/` directory. |

```powershell
go test -tags provider_smoke ./internal/coderepair/runtimebridge -run '^TestNativeProviderSmoke$' -count=1 -timeout 10m
```

Keep the report, its append-only `.manifest.jsonl` recovery journal, retained database schemas and fixture checkout, original campaign, and credential key together for recovery and audit. The journal is flushed before database initialization and before inference so an interrupted process retains the allocated schema identifiers, approved base, and case identity. The final report contains the outcome, patch digest, regression exits, explicit test-run flags, and runtime accounting; a test that did not run has a `null` exit code. Provider credentials and raw responses are omitted. Local tariff-based accounting is not a reconciled provider invoice.

The ordinary invocation rejects any campaign with a previous repair request. A failed attempt must be investigated offline first. After an operator explicitly approves a tested context correction, one new attempt can acknowledge the single retained `CONTEXT_LIMIT` failure: set `RUN_NATIVE_PROVIDER_SMOKE=one-approved-case-after-reviewed-context-fix`, `LIVE_ACKNOWLEDGED_FAILURE_PATH` to its private report, and `LIVE_ACKNOWLEDGED_ATTEMPT_ID` to its exact attempt ID. The guard verifies the report against the original accounted ledger receipt before opening credentials. It requires unchanged campaign limits and history, known usage, no held reservations, and no intervening request. It rejects a successful, uncertain, unrelated, or additional previous attempt. This is a separate operator decision, never an automatic retry. Changing a report path, deleting accounting records, or switching campaign IDs cannot create another allowance. Publication remains a separate operator-approved workflow.
