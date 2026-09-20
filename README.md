# Triovexa

Triovexa adalah operator incident triage berbasis Go. Project ini menerima alert, membuat incident, mengumpulkan evidence dari sistem observability, mengambil runbook dan postmortem yang relevan, membuat hasil triage, mengusulkan remediation action, mengevaluasi policy, meminta approval, mengeksekusi action yang aman, memverifikasi hasilnya, dan melakukan rollback jika action tertentu gagal.

Fokus utama project ini adalah **safety, auditability, dan local demoability**. AI dipakai untuk membantu analisis dan rekomendasi, tetapi eksekusi tetap dikontrol oleh action catalog, policy engine, approval gate, kill switch, timeout, retry, cooldown, verification, dan audit trail.

## Tujuan Project

Triovexa dibuat untuk mensimulasikan dan membangun fondasi sistem incident response yang lebih otomatis, tetapi tetap terkontrol.

Masalah yang ingin diselesaikan:

- alert dari Grafana sering hanya memberi gejala, bukan diagnosis
- operator perlu membuka banyak sumber data: metrics, logs, deployment history, runbook, dan postmortem
- rekomendasi action perlu dibatasi agar tidak membahayakan sistem
- setiap keputusan dan eksekusi perlu tercatat untuk audit
- remediation tidak cukup hanya "jalan"; hasilnya harus diverifikasi
- rollback dan escalation harus menjadi bagian eksplisit dari workflow

Target akhirnya adalah operator yang bisa membantu SRE/devops menangani incident dari awal sampai akhir:

```text
Alert masuk
  -> incident dibuat
  -> evidence dikumpulkan
  -> dokumen operasional dicari
  -> triage dibuat
  -> action diusulkan
  -> policy mengevaluasi action
  -> operator approve jika diperlukan
  -> action dieksekusi
  -> hasil diverifikasi
  -> incident resolved, escalated, atau rolled back
```

## Cara Kerja End-to-End

### 1. Alert Masuk Dari Grafana

Grafana mengirim alert ke Triovexa melalui webhook:

```text
POST /webhooks/grafana
```

Endpoint ini menerima payload Grafana-compatible. Payload kemudian dinormalisasi oleh modul `internal/alerting`.

Field penting yang dibaca dari payload:

- `title`
- `state`
- `commonLabels`
- `alerts`
- `alerts[].status`
- `alerts[].labels`
- `alerts[].annotations`
- `alerts[].fingerprint`
- `alerts[].startsAt`

Label yang paling penting:

- `service` atau `service_name`
- `environment` atau `env`
- `severity`

Fingerprint Grafana dipakai sebagai `external_alert_id`. Ini dipakai untuk deduplication, supaya alert yang sama tidak membuat banyak incident aktif.

Jika alert status-nya `firing`, Triovexa membuat incident baru atau memakai incident aktif yang sudah ada.

Jika alert status-nya `resolved`, Triovexa mencoba menutup atau mengubah state incident aktif yang terkait.

### 2. Incident Dibuat

Setelah alert dinormalisasi, Triovexa membuat entity incident dengan data seperti:

- ID internal
- external alert ID dari Grafana
- alert source
- service name
- environment
- severity
- title
- state awal: `detected`
- timestamp

Incident disimpan ke repository. Repository bisa berupa:

- PostgreSQL untuk mode normal
- in-memory store untuk demo cepat

### 3. Triage Dimulai

Incident kemudian masuk ke state:

```text
triaging
```

Pada tahap ini, incident service menjalankan beberapa langkah read-only:

1. context collection
2. knowledge retrieval
3. triage generation
4. candidate action generation
5. policy evaluation

Setiap tahap membuat audit event. Jika satu tahap gagal sebagian, hasilnya tetap dicatat agar operator tahu apa yang berhasil dan apa yang gagal.

### 4. Evidence Dikumpulkan

Triovexa punya dua mode observability:

```text
demo
grafana
```

Mode ini bisa dikontrol lewat runtime UI atau environment variable:

```text
OBSERVABILITY_MODE=demo
OBSERVABILITY_MODE=grafana
```

#### Demo Mode

Pada mode `demo`, evidence diambil dari demo service lokal:

```text
http://localhost:8090
```

Demo service mensimulasikan kondisi seperti:

- timeout setelah deploy
- worker stall
- error rate spike
- queue backlog
- worker unhealthy
- cache issue

Mode ini dipakai agar project bisa dicoba tanpa Grafana sungguhan.

#### Grafana Mode

Pada mode `grafana`, Triovexa mengambil evidence dari Grafana API. Alert tetap masuk lewat webhook, tetapi evidence diambil melalui datasource proxy Grafana.

Alur integrasinya:

```text
Grafana Alert
  -> webhook ke Triovexa
Triovexa
  -> Grafana API
  -> Prometheus datasource proxy untuk metrics
  -> Loki datasource proxy untuk logs
```

Endpoint Grafana yang digunakan:

```text
GET /api/datasources
GET /api/datasources/proxy/uid/{metrics_uid}/api/v1/query
GET /api/datasources/proxy/uid/{logs_uid}/loki/api/v1/query_range
```

Triovexa menggunakan Bearer token:

```text
Authorization: Bearer <GRAFANA_API_TOKEN>
```

Query Prometheus dipakai untuk metrics seperti:

- error rate
- latency
- queue backlog
- replica count

Query Loki dipakai untuk:

- application logs
- deployment logs

Query template mendukung placeholder:

```text
{{service}}
{{environment}}
{{severity}}
{{title}}
```

Contoh:

```text
sum(rate(http_requests_total{service="{{service}}", environment="{{environment}}", status=~"5.."}[5m]))
```

### 5. Runbook dan Postmortem Dicari

Knowledge retriever membaca file Markdown dari folder:

```text
docs/
```

Dokumen yang relevan dipilih berdasarkan keyword dari:

- service name
- environment
- severity
- title incident
- snippet evidence

Jenis dokumen yang bisa ditemukan:

- runbook di `docs/runbooks/`
- postmortem di `docs/postmortems/`
- dokumen operasi lain di `docs/operations/`

Retriever memberi skor sederhana berdasarkan kecocokan keyword di nama file dan isi dokumen, lalu mengambil dokumen paling relevan.

### 6. Triage Dibuat

Triovexa punya dua mode reasoning:

```text
heuristic
mistral
```

Mode ini bisa dikontrol lewat runtime UI atau environment variable:

```text
REASONING_MODE=heuristic
REASONING_MODE=mistral
```

#### Heuristic Mode

Mode `heuristic` memakai rule lokal di kode. Ini cocok untuk demo karena tidak butuh API key.

Heuristic generator melihat evidence dan dokumen, lalu membuat:

- summary
- hypotheses
- impact
- next steps
- confidence

#### Mistral Mode

Mode `mistral` memakai Mistral API untuk reasoning.

Environment variable yang dibutuhkan:

```text
MISTRAL_API_KEY
MISTRAL_MODEL
```

Walaupun reasoning memakai AI, action tetap harus melewati catalog dan policy. AI tidak diberi jalur bebas untuk mengeksekusi sesuatu.

### 7. Candidate Action Dibuat

Setelah triage, Triovexa menghasilkan candidate remediation action.

Action yang boleh dipertimbangkan harus ada di action catalog:

```text
internal/execution/catalog.go
```

Contoh action:

```text
restart_demo_worker
retry_demo_background_job
refresh_demo_cache
pause_demo_queue_consumer
resume_demo_queue_consumer
rollback_production_deployment
reroute_traffic
disable_primary_feature_flag
```

Setiap action punya metadata:

- key
- description
- risk level
- approval required
- executable
- allowed environments
- allowed targets
- max execution attempts
- execution cooldown
- rollback action
- parameters

Action yang tidak valid atau tidak ada di catalog akan ditandai invalid atau ditolak.

### 8. Policy Mengevaluasi Action

Policy engine mengevaluasi candidate action sebelum action bisa dipakai.

Policy mengecek:

- action ada di catalog atau tidak
- global kill switch aktif atau tidak
- environment diperbolehkan atau tidak
- target resource diperbolehkan atau tidak
- risk level action
- apakah action butuh approval
- apakah medium-risk action punya dependency metadata
- apakah medium-risk action punya rollback plan
- apakah rollback action tersedia
- apakah high-risk action harus diblok

Risk level:

```text
low
medium
high
```

Pada fase sekarang:

- low-risk action tetap butuh approval
- medium-risk action butuh approval dan rollback plan
- high-risk action diblok

Keputusan policy bisa berupa:

```text
allow
approval_required
deny
```

### 9. Operator Melakukan Approval

Jika policy memutuskan `approval_required`, incident masuk ke state:

```text
awaiting_approval
```

Operator bisa approve atau reject action dari UI.

Approval penting karena project ini memang didesain sebagai AI-assisted workflow, bukan fully autonomous remediation tanpa kontrol manusia.

### 10. Action Dieksekusi

Setelah action approved, execution service menjalankan action melalui adapter.

Proteksi saat eksekusi:

- kill switch dicek ulang
- action harus approved
- action harus executable
- environment dan target dicek ulang
- duplicate execution dicegah
- timeout diterapkan
- retry diterapkan jika error retryable
- cooldown diterapkan agar action tidak ditembak berkali-kali

Saat ini adapter eksekusi yang tersedia adalah demo adapter. Artinya action mengubah state demo service, bukan benar-benar melakukan operasi production.

### 11. Hasil Diverifikasi

Setelah action selesai, Triovexa masuk ke state:

```text
verifying_action
```

Verification service mengambil snapshot sebelum dan sesudah action. Lalu hasilnya diklasifikasikan:

```text
success
failed
inconclusive
```

Jika berhasil, incident bisa menjadi:

```text
resolved
```

Jika gagal, incident bisa menjadi:

```text
failed_remediation
```

Jika tidak jelas, incident bisa menjadi:

```text
escalated
```

### 12. Rollback atau Escalation

Untuk action tertentu yang punya rollback plan, Triovexa bisa menjalankan rollback otomatis.

Contoh:

```text
pause_demo_queue_consumer
  -> rollback action: resume_demo_queue_consumer
```

Rollback hanya dilakukan jika action catalog menyatakan action tersebut mendukung rollback.

Jika tidak ada rollback plan, atau hasil tetap tidak aman, incident akan dieskalasi agar manusia mengambil alih.

## State Machine Incident

Incident lifecycle dikontrol oleh state machine:

```text
detected
triaging
action_proposed
awaiting_approval
approved
executing_action
verifying_action
resolved
failed_remediation
rolled_back
escalated
closed
```

Prinsipnya:

- incident tidak boleh langsung lompat dari alert ke execution
- policy decision harus dibuat sebelum approval/execution
- verification adalah state eksplisit
- rollback dan escalation dicatat sebagai state nyata
- closing hanya boleh dari terminal state yang valid

## Arsitektur Teknis

Triovexa adalah Go backend monolith dengan UI server-rendered.

Stack utama:

```text
Language: Go
HTTP server: net/http
Database: PostgreSQL atau in-memory repository
Container: Docker Compose
Observability: demo service atau Grafana API
AI provider: heuristic local atau Mistral
UI: server-rendered HTML dan CSS
Metrics: /metrics endpoint
Config: environment variables
```

Entry point:

```text
cmd/server/main.go
cmd/demo-service/main.go
```

Modul utama:

```text
internal/alerting       Grafana webhook normalization
internal/incident       incident orchestration
internal/observability  demo/Grafana evidence collection
internal/retrieval      runbook/postmortem retrieval
internal/triage         heuristic/Mistral triage generation
internal/remediation    candidate action generation
internal/policy         action policy evaluation
internal/approval       approval and kill switch
internal/execution      controlled action execution
internal/verification   post-action verification and rollback
internal/storage        PostgreSQL and memory repository
internal/http           API, UI, diagnostics
internal/telemetry      internal metrics
internal/domain         core domain models
```

## Integrasi Grafana

Triovexa berintegrasi dengan Grafana lewat dua jalur.

### Jalur 1: Grafana Mengirim Alert Ke Triovexa

Grafana contact point atau notification policy diarahkan ke:

```text
http://<triovexa-host>:8080/webhooks/grafana
```

Untuk local development:

```text
http://localhost:8080/webhooks/grafana
```

Jika Grafana berjalan di tempat lain, pastikan host Triovexa bisa dijangkau dari Grafana.

Payload Grafana minimal harus punya `alerts`.

Label yang direkomendasikan:

```text
service
environment
severity
```

Contoh payload sederhana:

```json
{
  "title": "Checkout latency is high",
  "state": "firing",
  "commonLabels": {
    "service": "checkout",
    "environment": "staging",
    "severity": "warning"
  },
  "alerts": [
    {
      "status": "firing",
      "fingerprint": "checkout-latency-staging",
      "labels": {
        "service": "checkout",
        "environment": "staging",
        "severity": "warning"
      },
      "annotations": {
        "summary": "Checkout latency is above threshold"
      },
      "startsAt": "2026-01-01T00:00:00Z"
    }
  ]
}
```

### Jalur 2: Triovexa Mengambil Evidence Dari Grafana

Untuk mengambil metrics/logs, Triovexa memanggil Grafana API menggunakan token.

Environment variable:

```text
GRAFANA_BASE_URL=
GRAFANA_API_TOKEN=
GRAFANA_METRICS_DATASOURCE_UID=
GRAFANA_LOGS_DATASOURCE_UID=
GRAFANA_ERROR_RATE_QUERY=
GRAFANA_LATENCY_QUERY=
GRAFANA_QUEUE_QUERY=
GRAFANA_REPLICA_QUERY=
GRAFANA_LOGS_QUERY=
GRAFANA_DEPLOY_LOGS_QUERY=
GRAFANA_QUERY_LOOKBACK=15m
```

Datasource UID bisa dicek dari UI:

```text
http://localhost:8080/ui/setup/observability
```

Halaman setup observability bisa:

- test connection ke Grafana
- melihat datasource
- test query
- menyimpan local profile di mesin developer
- menghapus local profile

## Integrasi Mistral

Mistral digunakan hanya jika reasoning mode diset ke:

```text
mistral
```

Environment variable:

```text
MISTRAL_API_KEY=
MISTRAL_MODEL=mistral-small-latest
```

Jika API key tidak diset, gunakan mode:

```text
heuristic
```

Mode heuristic tetap bisa menjalankan demo end-to-end.

## Menjalankan Lokal

Prerequisite:

- Go 1.23 atau lebih baru
- Docker Desktop
- PowerShell

Start local stack:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev-start.ps1
```

Script ini akan:

- memastikan PostgreSQL Docker berjalan
- menjalankan demo service di port `8090`
- menjalankan app server di port `8080`
- membuka proses di PowerShell window terpisah

Endpoint utama:

```text
UI incident:          http://localhost:8080/ui/incidents
Observability setup:  http://localhost:8080/ui/setup/observability
Health:               http://localhost:8080/health
Metrics:              http://localhost:8080/metrics
Debug tools:          http://localhost:8080/debug/tools
Debug policies:       http://localhost:8080/debug/policies
Demo service state:   http://localhost:8090/state
```

PostgreSQL lokal project ini diekspos di port `5433` untuk menghindari konflik dengan PostgreSQL lokal lain:

```text
postgres://postgres:postgres@localhost:5433/triovexa?sslmode=disable
```

Jika ingin start database saja:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev-up.ps1
```

Jika ingin stop container:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev-down.ps1
```

Jika ingin cek status:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev-status.ps1
```

## Mode In-Memory

Untuk demo cepat tanpa PostgreSQL:

```powershell
$env:DATABASE_URL='memory'
powershell -ExecutionPolicy Bypass -File .\scripts\dev-start.ps1 -SkipDatabase
```

Catatan:

- data hanya hidup selama process berjalan
- cocok untuk demo cepat
- tidak cocok untuk audit/persistence nyata

## Demo Scenario

Demo scenario mensimulasikan alert dan incident tanpa Grafana sungguhan.

Jalankan salah satu:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\demo-scenario.ps1 -Scenario timeout-after-deploy
powershell -ExecutionPolicy Bypass -File .\scripts\demo-scenario.ps1 -Scenario worker-stall
powershell -ExecutionPolicy Bypass -File .\scripts\demo-scenario.ps1 -Scenario error-rate-spike
```

Scenario yang tersedia:

- `timeout-after-deploy`
- `worker-stall`
- `error-rate-spike`

Dokumen scenario:

- [docs/scenarios/timeout-after-deploy.md](docs/scenarios/timeout-after-deploy.md)
- [docs/scenarios/worker-stall-rollback.md](docs/scenarios/worker-stall-rollback.md)
- [docs/scenarios/error-rate-spike.md](docs/scenarios/error-rate-spike.md)

Walkthrough lengkap:

- [docs/operations/demo-guide.md](docs/operations/demo-guide.md)

## Cara Menggunakan UI

Buka:

```text
http://localhost:8080/ui/incidents
```

Dari halaman incident workbench, operator bisa:

- melihat daftar incident
- membuka detail incident
- melihat evidence
- membaca triage result
- melihat runbook/postmortem yang ditemukan
- melihat candidate action
- melihat policy decision
- approve action
- execute action
- melihat verification result
- melihat audit timeline
- mengaktifkan atau menonaktifkan kill switch
- mengganti reasoning mode
- mengganti observability mode
- menjalankan demo scenario

## Endpoint Penting

```text
GET  /health
GET  /metrics
GET  /debug/tools
GET  /debug/policies
POST /webhooks/grafana
GET  /incidents
GET  /incidents/{id}
GET  /incidents/{id}/actions
POST /actions/{id}/approve
POST /actions/{id}/reject
POST /actions/{id}/execute
POST /admin/kill-switch
POST /admin/runtime-modes
GET  /ui/incidents
GET  /ui/incidents/{id}
GET  /ui/setup/observability
POST /ui/setup/observability/test-connection
POST /ui/setup/observability/test-query
POST /ui/setup/observability/save-profile
POST /ui/setup/observability/clear-profile
```

## Environment Variables

Core config:

```text
APP_NAME=triovexa
APP_ENV=local
HTTP_PORT=8080
DATABASE_URL=postgres://postgres:postgres@localhost:5433/triovexa?sslmode=disable
DOCS_ROOT=docs
DEMO_SERVICE_BASE_URL=http://localhost:8090
REASONING_MODE=heuristic
OBSERVABILITY_MODE=demo
LOG_LEVEL=info
KILL_SWITCH_ENABLED=false
```

Execution config:

```text
ACTION_EXECUTION_TIMEOUT=5s
ACTION_EXECUTION_COOLDOWN=1m
ACTION_EXECUTION_RETRIES=1
```

HTTP timeout config:

```text
HTTP_READ_TIMEOUT=5s
HTTP_WRITE_TIMEOUT=30s
HTTP_IDLE_TIMEOUT=30s
HTTP_SHUTDOWN_TIMEOUT=10s
```

Demo service config:

```text
DEMO_SERVICE_PORT=8090
```

Grafana config:

```text
GRAFANA_BASE_URL=
GRAFANA_API_TOKEN=
GRAFANA_METRICS_DATASOURCE_UID=grafanacloud-prom
GRAFANA_LOGS_DATASOURCE_UID=grafanacloud-logs
GRAFANA_QUERY_LOOKBACK=15m
GRAFANA_ERROR_RATE_QUERY=
GRAFANA_LATENCY_QUERY=
GRAFANA_QUEUE_QUERY=
GRAFANA_REPLICA_QUERY=
GRAFANA_LOGS_QUERY=
GRAFANA_DEPLOY_LOGS_QUERY=
```

Mistral config:

```text
MISTRAL_API_KEY=
MISTRAL_MODEL=mistral-small-latest
```

Example config:

- [config/app.example.env](config/app.example.env)

## Safety Model

Triovexa tidak memberi akses bebas kepada AI untuk menjalankan command.

Safety layer:

- action harus ada di action catalog
- environment harus masuk allowlist
- target resource harus masuk allowlist
- high-risk action diblok
- medium-risk action membutuhkan metadata dependency
- medium-risk action membutuhkan rollback plan
- approval gate wajib untuk action yang berisiko
- global kill switch bisa memblok semua execution
- execution melakukan revalidation sebelum adapter dipanggil
- timeout mencegah action menggantung
- retry dibatasi
- cooldown mencegah repeated execution
- verification memastikan action benar-benar memperbaiki kondisi
- rollback hanya berjalan untuk action yang punya rollback plan
- semua langkah dicatat sebagai audit event

## Testing

Jalankan:

```powershell
go test ./...
go vet ./...
```

Coverage test mencakup:

- webhook intake
- incident deduplication
- resolved alert handling
- triage flow
- action generation
- policy decision
- approval workflow
- execution workflow
- verification workflow
- rollback path
- kill switch
- metrics endpoint
- diagnostics endpoint
- runtime mode switching
- observability setup UI

Strategi testing:

- [docs/operations/testing-strategy.md](docs/operations/testing-strategy.md)

## Batasan Saat Ini

- mode `grafana` membutuhkan query template yang cocok dengan schema metrics/logs milik user
- project tidak bisa menebak nama metric Prometheus atau label Loki secara universal
- datasource discovery bisa berhasil walaupun query mengembalikan data kosong
- auth untuk approval dan execution endpoint belum diimplementasikan
- execution adapter real untuk Kubernetes/cloud/provider production belum tersedia
- distributed tracing belum tersedia
- Mistral mode membutuhkan API key
- production high-risk action masih diblok

## Dokumentasi Tambahan

- [docs/architecture/high-level.md](docs/architecture/high-level.md)
- [docs/architecture/incident-state-machine.md](docs/architecture/incident-state-machine.md)
- [docs/architecture/workflow-engine-evaluation.md](docs/architecture/workflow-engine-evaluation.md)
- [docs/operations/demo-environment.md](docs/operations/demo-environment.md)
- [docs/operations/action-catalog-v1.md](docs/operations/action-catalog-v1.md)
- [docs/operations/policy-rules-v1.md](docs/operations/policy-rules-v1.md)
- [docs/operations/postgresql-setup.md](docs/operations/postgresql-setup.md)
- [docs/operations/testing-strategy.md](docs/operations/testing-strategy.md)

## License

Project ini open source di bawah MIT License. Lihat [LICENSE](LICENSE).
