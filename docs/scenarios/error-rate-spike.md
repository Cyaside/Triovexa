# Scenario: Error Rate Spike

## Goal

Demonstrate a lighter scenario that still produces evidence-based triage and constrained candidate actions.

## Trigger

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\demo-scenario.ps1 -Scenario error-rate-spike
```

## Expected Evidence

- the demo mode switches to `error_rate_spike`
- error rate and latency increase
- no rollback workflow is needed

## Expected Candidate Action

- `refresh_demo_cache` when latency is high enough

## Expected Policy Outcome

- `approval_required`

## Expected Verification Outcome

- depends on the chosen action, but approval, execution, and audit trail should still be visible

## What To Show During The Demo

- triage stays evidence-based even in the simpler scenario
- the policy engine still constrains actions
- the audit trail remains complete
