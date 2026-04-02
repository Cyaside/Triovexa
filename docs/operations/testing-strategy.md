# Testing Strategy

This document captures how Triovexa is tested. The goal is not only to prove that the happy path works, but also to ensure the safety guardrails are hard to bypass.

## Principles

- prioritize end-to-end flows that reflect a real incident lifecycle
- test safety rules as seriously as success paths
- use demo adapters and in-memory repositories to keep tests fast
- encode important failure modes in tests so regressions are easier to catch

## Functional Testing

Already automated:

- webhook intake
- incident creation
- triage generation
- candidate action generation
- policy decision
- approval workflow
- execution workflow
- verification workflow
- rollback workflow

Primary references:

- `internal/http/server_test.go`
- `internal/approval/service_test.go`
- `internal/execution/service_test.go`
- `internal/verification/service_test.go`

## Integration Testing

Already automated:

- Grafana-compatible webhook -> incident intake -> triage -> candidate actions
- approval -> execution -> verification -> incident state transition
- medium-risk execution -> verification failure -> rollback -> rolled_back state
- metrics and diagnostics endpoints after workflows run

Because the demo mode remains local-first, these integration tests use:

- `httptest.Server`
- `MemoryStore`
- demo HTTP adapter

## Safety Testing

Already automated:

- actions outside the catalog are denied by policy
- actions cannot execute without approval
- the kill switch blocks execution
- duplicate execution is prevented by the idempotency guard
- max attempts and cooldowns are enforced in the execution layer
- execution scope is revalidated before the adapter is called
- missing action endpoints return `404`

## Failure Testing

Already automated:

- retryable adapter errors during execution
- verification `failed`
- verification `inconclusive`
- successful rollback after verification failure
- failed rollback still leads to an escalation recommendation

Useful additions for a future iteration:

- real PostgreSQL integration tests with containers
- collector failure from a real observability source
- AI provider timeouts once external providers are always enabled
- webhook secret validation failure
- authorization failure for approval and execution endpoints

## How To Run

Run the full suite:

```powershell
go test ./...
```

Run light static analysis:

```powershell
go vet ./...
```

Run the most commonly touched packages:

```powershell
go test ./internal/http ./internal/approval ./internal/execution ./internal/verification
```

## Demo-Ready Exit Criteria

Before a presentation or demo recording:

- `go test ./...` must pass
- `go vet ./...` must pass
- the `timeout-after-deploy` scenario must end in `resolved`
- the `worker-stall` scenario must demonstrate a successful rollback
- `/metrics`, `/debug/tools`, and `/debug/policies` must be reachable
