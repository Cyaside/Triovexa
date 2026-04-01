# Demo Environment

Service demo ini disiapkan untuk menghasilkan sinyal insiden yang bisa dipakai pada phase intake, triage, dan remediation berikutnya.

## Komponen

- `cmd/demo-service`
  Service HTTP kecil yang bisa berpindah mode dari sehat ke beberapa kondisi insiden.

## Endpoint

- `GET /health`
- `GET /state`
- `GET /metrics`
- `POST /simulate/error-rate-spike`
- `POST /simulate/worker-stall`
- `POST /simulate/timeout-after-deploy`
- `POST /simulate/reset`
- `POST /actions/restart-worker`
- `POST /actions/retry-job`
- `POST /actions/refresh-cache`
- `POST /actions/pause-queue-consumer`
- `POST /actions/resume-queue-consumer`

## Mode Insiden

- `healthy`
- `error_rate_spike`
- `worker_stall`
- `timeout_after_deploy`

## Contoh Menjalankan

```powershell
go run ./cmd/demo-service
go run ./cmd/server
```

Sebelum menjalankan server utama, pastikan `DATABASE_URL` sudah mengarah ke PostgreSQL yang aktif.

Cara paling cepat:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev-up.ps1
```

Cara paling praktis untuk menyalakan semuanya sekaligus:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev-start.ps1
```

Jika hanya ingin menyalakan proses aplikasi tanpa bootstrap database ulang:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev-start.ps1 -SkipDatabase
```

Jika skrip memberi error bahwa Docker daemon tidak reachable, nyalakan Docker Desktop dulu lalu jalankan ulang.

## Contoh Memicu Insiden

```powershell
Invoke-WebRequest -Method Post http://localhost:8090/simulate/error-rate-spike
Invoke-WebRequest -Method Post http://localhost:8090/simulate/worker-stall
Invoke-WebRequest -Method Post http://localhost:8090/simulate/reset
```

## Tujuan Praktis

- menyediakan target untuk alert rule Grafana
- menyediakan metrics sederhana untuk observability
- menyediakan kondisi yang bisa dipakai saat demo triage dan candidate action

## Endpoint UI dan API Phase 6

- `GET /ui/incidents`
- `GET /ui/incidents/{id}`
- `POST /webhooks/grafana`
- `GET /incidents`
- `GET /incidents/{id}`
- `GET /incidents/{id}/triage`
- `GET /incidents/{id}/actions`
- `GET /actions/{id}/verification`
- `GET /actions/{id}/rollbacks`
- `POST /actions/{id}/approve`
- `POST /actions/{id}/reject`
- `POST /actions/{id}/execute`
- `POST /admin/kill-switch`

## Environment Variables

Yang aktif dipakai saat ini:

- `DATABASE_URL`
- `DOCS_ROOT`
- `DEMO_SERVICE_BASE_URL`
- `HTTP_PORT`
- `LOG_LEVEL`
- `KILL_SWITCH_ENABLED`
- `ACTION_EXECUTION_TIMEOUT`
- `ACTION_EXECUTION_COOLDOWN`
- `ACTION_EXECUTION_RETRIES`

Yang belum wajib sekarang, tetapi nanti perlu Anda isi saat kita sambungkan ke integrasi eksternal sungguhan:

- `GRAFANA_BASE_URL`
  Dipakai jika kita ingin mengambil konteks tambahan dari Grafana API, bukan hanya menerima webhook.
- `GRAFANA_API_TOKEN`
  Token akses untuk query dashboard, alert detail, atau API pendukung Grafana.
- `GRAFANA_WEBHOOK_SECRET`
  Secret untuk verifikasi request webhook Grafana agar intake lebih aman.
- `GOOGLE_API_KEY`
  Kredensial untuk provider model Google saat heuristik lokal diganti dengan model sungguhan.
- `GOOGLE_MODEL`
  Nama model Google yang akan dipakai untuk triage dan candidate action generation.

Selama variable external integration di atas belum diisi, aplikasi tetap jalan dengan mode heuristik lokal yang kita pakai sekarang.

## Contoh Approval, Execution, dan Kill Switch

Approve action dari API:

```powershell
Invoke-RestMethod -Method Post `
  -Uri http://localhost:8080/actions/<ACTION_ID>/approve `
  -ContentType "application/json" `
  -Body '{"approved_by":"operator-a","note":"safe to proceed"}'
```

Reject action dari API:

```powershell
Invoke-RestMethod -Method Post `
  -Uri http://localhost:8080/actions/<ACTION_ID>/reject `
  -ContentType "application/json" `
  -Body '{"approved_by":"operator-a","note":"needs manual investigation"}'
```

Execute action low-risk yang sudah approved:

```powershell
Invoke-RestMethod -Method Post `
  -Uri http://localhost:8080/actions/<ACTION_ID>/execute `
  -ContentType "application/json" `
  -Body '{"initiated_by":"operator-a","note":"execute approved action"}'
```

Execute action medium-risk yang sudah approved:

```powershell
Invoke-RestMethod -Method Post `
  -Uri http://localhost:8080/actions/<ACTION_ID>/execute `
  -ContentType "application/json" `
  -Body '{"initiated_by":"operator-b","note":"execute medium-risk action with rollback plan"}'
```

Lihat hasil verification untuk action tersebut:

```powershell
Invoke-RestMethod -Method Get `
  -Uri http://localhost:8080/actions/<ACTION_ID>/verification
```

Lihat rollback record untuk action yang sama:

```powershell
Invoke-RestMethod -Method Get `
  -Uri http://localhost:8080/actions/<ACTION_ID>/rollbacks
```

Aktifkan kill switch:

```powershell
Invoke-RestMethod -Method Post `
  -Uri http://localhost:8080/admin/kill-switch `
  -ContentType "application/json" `
  -Body '{"enabled":true}'
```

## Closed-Loop Verification Yang Sudah Aktif

Setelah `POST /actions/{id}/execute` berhasil, server utama sekarang otomatis akan:

- mengambil snapshot kondisi layanan sesudah action
- membandingkan sinyal sebelum dan sesudah action
- menyimpan `verification_result`
- memindahkan incident ke `resolved` jika sinyal membaik kuat
- memindahkan incident ke `escalated` jika hasil gagal atau tidak meyakinkan
- memicu rollback otomatis untuk medium-risk action yang punya rollback plan jika verification gagal
- memindahkan incident ke `rolled_back` jika rollback berhasil

Rule awal yang dipakai tetap sederhana dan mudah diaudit:

- alert dianggap clear jika mode demo kembali `healthy`
- health check dianggap normal jika `worker_healthy=true`
- improvement dihitung dari `error_rate`, `latency_ms`, dan `queue_backlog`
- mixed signal tidak akan auto-resolve, tetapi akan di-escalate
