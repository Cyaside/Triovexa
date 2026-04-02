# Scenario: Timeout After Deploy

## Goal

Demonstrate a full triage-to-verification success path for the low-risk action `refresh_demo_cache`.

## Trigger

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\demo-scenario.ps1 -Scenario timeout-after-deploy
```

## Expected Evidence

- the demo mode switches to `timeout_after_deploy`
- latency and error rate increase
- fresh deployment context appears
- the matching runbook and postmortem are retrieved

## Expected Candidate Action

- `refresh_demo_cache`

## Expected Policy Outcome

- `approval_required`

## Expected Verification Outcome

- the action is executed
- verification status becomes `success`
- the incident moves to `resolved`

## What To Show During The Demo

- deployment and metric evidence
- the rationale for choosing cache refresh
- execution record
- verification result
- internal counters in `/metrics`
