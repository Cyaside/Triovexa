# Scenario: Worker Stall With Rollback

## Goal

Demonstrate the strongest closed-loop path in the demo: a medium-risk, approval-gated action followed by automatic rollback after failed verification.

## Trigger

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\demo-scenario.ps1 -Scenario worker-stall
```

## Expected Evidence

- the demo mode switches to `worker_stall`
- queue backlog rises
- the worker becomes unhealthy
- the worker-stall runbook and postmortem are retrieved

## Expected Candidate Actions

- `restart_demo_worker`
- `retry_demo_background_job`
- `pause_demo_queue_consumer`

## Expected Policy Outcome

- the medium-risk action `pause_demo_queue_consumer` remains `approval_required`
- the policy output highlights dependency awareness and the rollback plan

## Expected Verification Outcome

- the medium-risk action is executed
- verification status becomes `failed`
- rollback `resume_demo_queue_consumer` is triggered automatically
- the incident moves to `rolled_back`

## What To Show During The Demo

- the medium-risk classification
- approval flow
- rollback records
- verification evidence showing that escalation is no longer recommended after rollback succeeds
