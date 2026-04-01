# Demo Guide

Panduan ini disiapkan untuk membantu orang lain menjalankan dan mendemokan Triovexa tanpa perlu dijelaskan panjang secara lisan.

## Prasyarat

- Docker Desktop aktif untuk PostgreSQL lokal
- Go terpasang
- PowerShell bisa menjalankan script lokal

## Menyalakan Environment

1. Jalankan database lokal:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev-up.ps1
```

2. Jalankan demo service dan server utama:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev-start.ps1
```

3. Cek health:

```powershell
Invoke-RestMethod http://localhost:8080/health
Invoke-RestMethod http://localhost:8090/health
```

4. Cek observability internal Triovexa:

```powershell
Invoke-WebRequest http://localhost:8080/metrics | Select-Object -ExpandProperty Content
Invoke-RestMethod http://localhost:8080/debug/tools
Invoke-RestMethod http://localhost:8080/debug/policies
```

## Menjalankan Skenario Demo

Cara paling cepat adalah memakai script helper:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\demo-scenario.ps1 -Scenario timeout-after-deploy
powershell -ExecutionPolicy Bypass -File .\scripts\demo-scenario.ps1 -Scenario worker-stall
powershell -ExecutionPolicy Bypass -File .\scripts\demo-scenario.ps1 -Scenario error-rate-spike
```

Script akan:

- memicu mode insiden pada demo service
- mengirim webhook Grafana-compatible ke server utama
- menampilkan `incident_id` yang baru dibuat

## Alur Presentasi yang Disarankan

### 1. Tunjukkan posture sistem

- buka `GET /debug/tools`
- buka `GET /debug/policies`
- jelaskan kill switch, catalog action, dan scope safety

### 2. Trigger insiden

- jalankan salah satu skenario dengan `demo-scenario.ps1`
- buka UI di `http://localhost:8080/ui/incidents`
- klik incident terbaru

### 3. Jelaskan triage

- tunjukkan summary, hypotheses, blast radius
- sorot evidence dan dokumen runbook/postmortem yang terambil
- jelaskan bahwa candidate action dibatasi ke catalog allowlist

### 4. Jelaskan policy dan approval

- lihat candidate action panel
- sorot risk level, rationale, dan approval hint
- approve salah satu action dari UI atau API

### 5. Eksekusi action

- jalankan action dari UI
- tunjukkan execution record dan verification result
- buka `/metrics` untuk menunjukkan counter execution dan verification ikut bergerak

### 6. Tunjukkan rollback bila perlu

- pakai skenario `worker-stall`
- approve dan execute `pause_demo_queue_consumer`
- tunjukkan incident berakhir di `rolled_back` jika verification gagal dan rollback otomatis berhasil

### 7. Tunjukkan kontrol operator

- aktifkan kill switch dari UI/API
- jelaskan bahwa policy dan approval tetap terlihat, tetapi action baru diblok

## Checklist Demo Singkat

- PostgreSQL aktif
- demo service aktif
- server Triovexa aktif
- `/metrics` mengembalikan metrik internal
- `/ui/incidents` bisa dibuka
- minimal satu skenario selesai end-to-end

## Catatan Manual

Variable eksternal berikut belum wajib untuk mode demo lokal saat ini:

- `GRAFANA_BASE_URL`
- `GRAFANA_API_TOKEN`
- `GRAFANA_WEBHOOK_SECRET`
- `GOOGLE_API_KEY`
- `GOOGLE_MODEL`

Kalau nanti variable itu sudah diisi, kita bisa ganti dari heuristic lokal ke integrasi observability dan provider AI sungguhan tanpa mengubah demo flow dasarnya.
