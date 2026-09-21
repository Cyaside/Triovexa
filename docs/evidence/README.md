# End-to-end evidence

The checked-in record [`worker-stall-e2e.json`](worker-stall-e2e.json) is the machine-readable output of `scripts/demo.ps1 -Reset` on 2026-09-21 UTC. Run `20260921T001541Z` tested commit `6e6a413`; all seven gates passed:

| Gate | Observed result |
| --- | --- |
| E2E-01 | PostgreSQL, Redis, and the workload supervisor passed dependency-aware readiness. |
| E2E-02 | The Redis Streams worker established a healthy baseline. |
| E2E-03 | A bounded stall created a real queue backlog. |
| E2E-04 | Prometheus fired and Alertmanager delivered the alert to Triovexa. |
| E2E-05 | Triage proposed the allowlisted `restart_worker` action. |
| E2E-06 | Approval and execution changed the real worker generation from 1 to 2. |
| E2E-07 | Verification observed backlog reduction and three consecutive healthy samples. |

The run lasted 122.6 seconds. The final workload state had backlog `0`, generation `2`, worker healthy, and `98` produced jobs equal to `98` processed jobs. The verification trace stores four samples (`65 → 10 → 0 → 0`) and requires three consecutive recovery observations rather than relying on the supervisor's HTTP response alone.

This evidence proves the included Compose scenario on the tested machine. It does not establish general production reliability, multi-tenant isolation, or compatibility with workloads outside the allowlisted supervisor contract.
