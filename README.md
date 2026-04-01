# Triovexa

Triovexa adalah implementasi `AI Incident Triage Operator` berbasis Go: sistem incident-response copilot yang menerima alert, mengumpulkan evidence, menarik runbook/postmortem, mengusulkan candidate action, mengevaluasi policy, menjalankan remediation yang diizinkan, lalu memverifikasi outcome dan rollback bila perlu.

Repositori ini mengikuti PRD bertahap dari `Phase 0` sampai `Phase 7`, dengan fokus kuat pada safety, auditability, dan demoability.

## Yang Sudah Ada

- Grafana-compatible webhook intake
- incident list dan incident detail UI
- evidence collection dari demo service
- retrieval runbook dan postmortem dari folder `docs/`
- heuristic triage dan heuristic candidate action generation
- policy engine dengan allowlist, risk classification, dan approval gate
- kill switch global
- low-risk execution pipeline dengan timeout, retry, cooldown, dan idempotency
- verification engine berbasis before/after signal comparison
- automatic rollback untuk medium-risk demo action tertentu
- audit trail persisten di PostgreSQL
- internal telemetry dan diagnostics endpoint (`/metrics`, `/debug/tools`, `/debug/policies`)

## Arsitektur Ringkas

```text
Grafana Alert
  -> Alert Receiver
  -> Incident Orchestrator
     -> Context Collectors
     -> Knowledge Retriever
     -> AI / Heuristic Layer
     -> Policy Engine
     -> Approval Layer
     -> Execution Engine
     -> Verification Engine
     -> Rollback / Escalation
  -> PostgreSQL Persistence + Audit Store
  -> Operator UI + Metrics + Diagnostics
```

Komponen utamanya tetap vendor-neutral di layer domain. Adapter observability, execution, dan verification dipisahkan dari orchestration core supaya flow tetap bisa dijelaskan dan diganti bertahap.

## Menjalankan Lokal

1. Nyalakan PostgreSQL lokal:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev-up.ps1
```

2. Jalankan semua proses dev:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev-start.ps1
```

3. Akses endpoint utama:

- UI: `http://localhost:8080/ui/incidents`
- Health: `http://localhost:8080/health`
- Metrics: `http://localhost:8080/metrics`
- Debug tools: `http://localhost:8080/debug/tools`
- Debug policies: `http://localhost:8080/debug/policies`
- Demo service state: `http://localhost:8090/state`

Konfigurasi contoh ada di [config/app.example.env](config/app.example.env).

## Demo Scenarios

Pakai script helper berikut untuk memicu skenario demo end-to-end:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\demo-scenario.ps1 -Scenario timeout-after-deploy
powershell -ExecutionPolicy Bypass -File .\scripts\demo-scenario.ps1 -Scenario worker-stall
powershell -ExecutionPolicy Bypass -File .\scripts\demo-scenario.ps1 -Scenario error-rate-spike
```

Skenario yang tersedia:

- [docs/scenarios/timeout-after-deploy.md](docs/scenarios/timeout-after-deploy.md)
- [docs/scenarios/worker-stall-rollback.md](docs/scenarios/worker-stall-rollback.md)
- [docs/scenarios/error-rate-spike.md](docs/scenarios/error-rate-spike.md)

Demo guide lengkap ada di [docs/operations/demo-guide.md](docs/operations/demo-guide.md).

## Testing

Perintah utama:

```powershell
go test ./...
go vet ./...
```

Strategi testing tertulis ada di [docs/operations/testing-strategy.md](docs/operations/testing-strategy.md).

Coverage yang sudah ada mencakup:

- webhook intake
- triage dan action generation flow
- policy decision
- approval workflow
- execution path
- verification path
- blocked action
- kill switch
- rollback path
- metrics dan diagnostics endpoint

## Environment Variables

Dipakai saat ini:

- `DATABASE_URL`
- `DOCS_ROOT`
- `DEMO_SERVICE_BASE_URL`
- `HTTP_PORT`
- `LOG_LEVEL`
- `KILL_SWITCH_ENABLED`
- `ACTION_EXECUTION_TIMEOUT`
- `ACTION_EXECUTION_COOLDOWN`
- `ACTION_EXECUTION_RETRIES`

Belum wajib untuk mode demo lokal, tetapi akan dibutuhkan saat integrasi eksternal sungguhan diaktifkan:

- `GRAFANA_BASE_URL`
- `GRAFANA_API_TOKEN`
- `GRAFANA_WEBHOOK_SECRET`
- `GOOGLE_API_KEY`
- `GOOGLE_MODEL`

## Safety Scope

- hanya action yang ada di catalog yang bisa dievaluasi
- high-risk action tetap diblok di fase awal
- medium-risk action tetap approval-gated
- execution revalidasi environment dan target sebelum adapter dipanggil
- verification membedakan `success`, `failed`, dan `inconclusive`
- rollback otomatis hanya berjalan untuk action yang memang punya rollback plan

## Batasan Saat Ini

- AI layer masih memakai heuristic generator lokal, belum provider AI sungguhan
- observability source masih demo adapter, belum Grafana/Loki real query path
- autentikasi endpoint approval/execution belum ditambahkan
- tracing distributed belum diaktifkan; phase saat ini baru mencakup structured logging dan metrics internal

## Dokumentasi Tambahan

- [docs/architecture/high-level.md](docs/architecture/high-level.md)
- [docs/architecture/incident-state-machine.md](docs/architecture/incident-state-machine.md)
- [docs/architecture/workflow-engine-evaluation.md](docs/architecture/workflow-engine-evaluation.md)
- [docs/operations/demo-environment.md](docs/operations/demo-environment.md)
- [docs/operations/action-catalog-v1.md](docs/operations/action-catalog-v1.md)
- [docs/operations/policy-rules-v1.md](docs/operations/policy-rules-v1.md)
