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

## Endpoint UI dan API Phase 1

- `GET /ui/incidents`
- `GET /ui/incidents/{id}`
- `POST /webhooks/grafana`
- `GET /incidents`
- `GET /incidents/{id}`
- `GET /incidents/{id}/triage`
