# Demo Guide

This guide helps other people run and demo Triovexa without needing a long verbal walkthrough.

## Prerequisites

- Docker Desktop running for local PostgreSQL
- Go installed
- PowerShell able to run local scripts

## Starting The Environment

1. Start the local database:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev-up.ps1
```

2. Start the demo service and main server:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev-start.ps1
```

3. Check health:

```powershell
Invoke-RestMethod http://localhost:8080/health
Invoke-RestMethod http://localhost:8090/health
```

4. Check Triovexa internal observability:

```powershell
Invoke-WebRequest http://localhost:8080/metrics | Select-Object -ExpandProperty Content
Invoke-RestMethod http://localhost:8080/debug/tools
Invoke-RestMethod http://localhost:8080/debug/policies
```

## Running Demo Scenarios

The fastest option is the helper script:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\demo-scenario.ps1 -Scenario timeout-after-deploy
powershell -ExecutionPolicy Bypass -File .\scripts\demo-scenario.ps1 -Scenario worker-stall
powershell -ExecutionPolicy Bypass -File .\scripts\demo-scenario.ps1 -Scenario error-rate-spike
```

The script will:

- trigger an incident mode on the demo service
- send a Grafana-compatible webhook to the main server
- print the newly created `incident_id`

## Suggested Demo Walkthrough

### 1. Show the system posture

- open `GET /debug/tools`
- open `GET /debug/policies`
- explain the kill switch, action catalog, and safety scope

### 2. Trigger an incident

- run one of the scenarios with `demo-scenario.ps1`
- open the UI at `http://localhost:8080/ui/incidents`
- open the newest incident

### 3. Explain triage

- show the summary, hypotheses, and blast radius
- highlight the evidence and retrieved runbook/postmortem documents
- explain that candidate actions are constrained to the allowlisted catalog

### 4. Explain policy and approval

- inspect the candidate action panel
- highlight risk level, rationale, and approval hints
- approve one action from the UI or API

### 5. Execute an action

- execute an action from the UI
- show the execution record and verification result
- open `/metrics` to show the execution and verification counters changing

### 6. Show rollback when needed

- use the `worker-stall` scenario
- approve and execute `pause_demo_queue_consumer`
- show the incident ending in `rolled_back` when verification fails and automatic rollback succeeds

### 7. Show operator controls

- enable the kill switch from the UI or API
- explain that policy and approval remain visible while new execution is blocked

## Quick Demo Checklist

- PostgreSQL is running
- the demo service is running
- the Triovexa server is running
- `/metrics` returns internal metrics
- `/ui/incidents` opens successfully
- at least one scenario completes end-to-end

## Manual Notes

These external variables are optional for the local demo, but needed when real integrations are enabled:

- `GRAFANA_BASE_URL`
- `GRAFANA_API_TOKEN`
- `GRAFANA_WEBHOOK_SECRET`
- `MISTRAL_API_KEY`
- `MISTRAL_MODEL`

Once those variables are populated, you can switch from local heuristics to real observability and AI providers without changing the core demo flow.
