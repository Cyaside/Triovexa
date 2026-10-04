# Code repair (staging)

Triovexa can prepare a bounded source patch for an escalated Redis worker incident. The operator authorizes repository investigation, reviews the resulting diff and red-to-green regression test, then separately authorizes publication of a **draft** GitHub pull request. GitHub CI and human review still control merge. The deployment pipeline remains responsible for rollout; Triovexa only marks the repair recovered after the deployed merge revision and three workload observations agree.

This integration currently targets one registered GitHub repository, a Go worker, and the `queue-worker` staging workload. Tests use administrator-registered recipes with fixed commands; the workload uses `go-test-workload`, and an independent parser fixture verifies the `go-test-positive-int` recipe. It does not run arbitrary tests, change CI files, auto-merge, or deploy code.

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

The normal Compose stack supplies PostgreSQL, the workload supervisor, and the UI. Configure an OpenAI-compatible reasoning connection in **Connections**. An administrator then opens an escalated incident's **Code repair** tab and registers the repository URL, protected base branch, and allowed paths. A proposal requires fresh incident evidence, including a trusted deployed revision and log signal.

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

Configure a GitHub `pull_request` webhook to `POST /webhooks/github/repair` with the same secret. Keep the publisher token out of the investigation worker and browser. The registered repository must currently be cloneable without a Git credential; private repository checkout is not yet supported.

The deployment pipeline sends two authenticated JSON requests to `POST /webhooks/deployment/repair` for the **merge commit SHA**, same case and deployment ID. Both the workload supervisor and Prometheus alert API must be reachable:

```json
{"case_id":"<case-id>","deployment_id":"<deployment-id>","environment":"staging","revision_sha":"<merge-sha>","phase":"started"}
```

Send `started` immediately before rollout so Triovexa captures the pre-deploy workload baseline. After the new revision is rolled out, send the same body with `"phase":"completed"`. The endpoint requires `Authorization: Bearer <REPAIR_DEPLOYMENT_TOKEN>`. The verifier then samples the supervisor and Prometheus every 10 seconds for up to two minutes. It requires the new revision, healthy worker, job progress, falling or empty backlog, no increase in errors, and the queue-backlog alert cleared in three consecutive samples. Missing or stale telemetry becomes `inconclusive`.

## Safety and recovery

Publication approval lasts 15 minutes and binds the case, repository scope, base commit, policy, attempt, and patch SHA-256. The publisher has its own credential and stable operation ID. After an uncertain GitHub response or process restart, it checks the dedicated branch and PR before writing again; a conflicting branch or PR blocks publication. Verified GitHub webhook deliveries are deduplicated and cannot authorize deployment.

The UI shows evidence, test proof, diff, approval, PR, deployment, and verification state. `patch_ready`, `pr_open`, and `merged` do **not** mean the incident recovered. The deployment pipeline and a reviewer remain separate decision makers. The deterministic CI gate uses a scripted model and fake GitHub API; a live provider and real GitHub staging run must be evaluated separately before relying on this workflow for operational use.
