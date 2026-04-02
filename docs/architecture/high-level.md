# High-Level Architecture

This document describes the implementation-level architecture of Triovexa.

## Core Components

1. `Alert Receiver`
   Accepts Grafana-compatible alert webhooks and normalizes the payload.

2. `Incident Orchestrator`
   Manages the incident lifecycle from `detected` to `closed`.

3. `Context Collectors`
   Collects logs, metrics, deployment signals, and service metadata.

4. `Knowledge Retriever`
   Retrieves relevant runbooks, postmortems, and operational documents.

5. `Reasoning Layer`
   Produces summaries, hypotheses, next steps, and candidate actions from the available evidence.

6. `Policy Engine`
   Decides whether a candidate action is allowed, requires approval, or must be denied.

7. `Approval Layer`
   Acts as the human-in-the-loop gate for selected actions.

8. `Execution Engine`
   Runs actions that passed policy and approval.

9. `Verification Engine`
   Checks whether an action improved the incident state.

10. `Internal Telemetry & Diagnostics`
    Provides structured logging, internal metrics, and diagnostics endpoints for operators.

11. `Persistence & Audit Store`
    Stores incidents, triage results, policy decisions, execution records, verification results, and audit events.

12. `Operator UI`
    Shows incident lists, incident detail, evidence, actions, approvals, and audit history.

## High-Level Flow

```text
Grafana Alert
  -> Alert Receiver
  -> Incident Orchestrator
     -> Context Collectors
     -> Knowledge Retriever
     -> AI Agent
     -> Policy Engine
     -> Approval Layer
     -> Execution Engine
     -> Verification Engine
  -> Internal Telemetry & Diagnostics
  -> Persistence & Audit Store
  -> Operator UI
```

## Boundaries

- vendor-specific integrations should live in dedicated adapters
- the orchestration core should not depend on vendor implementation details
- the reasoning provider should be replaceable without changing the incident domain
- write paths must go through controlled execution adapters
- policy decisions and approvals must always be persisted

## Implemented Foundations

- HTTP service bootstrap
- core domain entities
- incident state machine
- initial action catalog
- baseline policy evaluator
- example application configuration
- PostgreSQL-backed persistence
- internal telemetry endpoints for metrics and diagnostics
