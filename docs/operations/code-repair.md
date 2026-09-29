# Code repair (staging)

Triovexa can prepare a bounded source patch for an escalated Redis worker incident. The operator authorizes repository investigation, reviews the resulting diff and red-to-green regression test, then separately authorizes publication of a **draft** GitHub pull request. GitHub CI and human review still control merge. The deployment pipeline remains responsible for rollout; Triovexa only marks the repair recovered after the deployed merge revision and three workload observations agree.

This integration currently targets one registered GitHub repository, a Go worker, the fixed `go-test-workload` recipe, and the `queue-worker` staging workload. It does not run arbitrary tests, change CI files, auto-merge, or deploy code.

## Run the components

The normal Compose stack supplies PostgreSQL, the workload supervisor, and the UI. Configure an OpenAI-compatible reasoning connection in **Connections**. An administrator then opens an escalated incident's **Code repair** tab and registers the repository URL, protected base branch, and allowed paths. A proposal requires fresh incident evidence, including a trusted deployed revision and log signal.

The investigation worker uses Docker Desktop from the host so the sandbox receives a private checkout without exposing the Docker socket to the Triovexa server. On Windows, run this in a dedicated terminal while the Compose stack is up:

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
