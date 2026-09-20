# Workflow Engine Evaluation

This note records the current evaluation of whether Triovexa still fits an internal orchestration model or should move to an external workflow engine such as Temporal.

## Current Decision

- decision: keep the internal orchestration approach
- status: accepted for the current action catalog
- revisit when the action catalog or recovery logic grows materially

## Why The Current Approach Is Still Enough

- the number of incident states is still limited and explicit
- retry and timeout behavior is simple and directly auditable in service code
- current rollback support covers only a short medium-risk compensation path
- there is no need yet for cross-process or long-running workflow resumption

## Signals That Should Trigger Re-evaluation

- retry policies diverge significantly between actions
- rollback needs multi-step compensation
- action chains start spanning more than one external system
- operators need workflow resumption after process crashes or deployments
- the audit trail needs to represent branching workflows that exceed the current state machine

## Risks Of Moving Too Early

- operational complexity increases before the use case is mature
- local debugging becomes slower
- an unstable domain model can grow around tool constraints too early

## Risks Of Waiting Too Long

- orchestration code becomes harder to read due to retry and compensation branches
- state transitions become scattered
- rollback and escalation logic may start overlapping as the action catalog grows

## Guardrails Until Re-evaluation

- keep a single primary orchestration service
- store audit events and state transitions explicitly
- avoid adding multi-step actions without a clear rollback plan
- document every new workflow that adds compensation or branching
