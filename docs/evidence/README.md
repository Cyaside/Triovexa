# End-to-end evidence

The checked-in record [`worker-stall-e2e.json`](worker-stall-e2e.json) is the sanitized machine-readable output of `scripts/demo.ps1 -SkipBuild` on 2026-09-21 UTC. Run `20260921T071146Z` tested commit `0e60840` with the persisted OpenAI-compatible connection and `glm-5.3-flash`; all seven gates passed without a heuristic fallback:

| Gate | Observed result |
| --- | --- |
| E2E-01 | PostgreSQL, Redis, and the workload supervisor passed dependency-aware readiness. |
| E2E-02 | The Redis Streams worker established a healthy baseline and the previous alert episode was clear. |
| E2E-03 | Fault episode `2` created a real queue backlog. |
| E2E-04 | Prometheus fired and Alertmanager delivered the episode-labelled alert to Triovexa. |
| E2E-05 | `glm-5.3-flash` generated triage and proposed the allowlisted `restart_worker` action. |
| E2E-06 | Approval and execution changed the real worker generation from 2 to 3. |
| E2E-07 | Verification observed backlog reduction and three consecutive healthy samples. |

The run lasted 221.0 seconds. Provider metadata records 34.0 seconds for triage and 52.4 seconds for remediation, including token usage returned by the endpoint. A fresh workload snapshot was collected between those calls so the approved action still passed the 60-second evidence freshness guard. Execution changed the real worker generation from `2` to `3`; the verification trace stores four decreasing-backlog samples (`148 → 93 → 38 → 0`) and the final captured workload state was healthy with backlog `1` as the producer continued running.

The provider API key is absent from the evidence. The record includes only provider name, model, credential availability/source, latency, token counts, action metadata, and operational observations.

This evidence proves the included Compose scenario on the tested machine. It does not establish general production reliability, multi-tenant isolation, or compatibility with workloads outside the allowlisted supervisor contract.
