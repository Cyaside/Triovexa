# Scenario: Worker Stall With Rollback

## Tujuan

Menunjukkan capability paling kuat sampai Phase 6: medium-risk approval-gated action dengan automatic rollback saat verification gagal.

## Trigger

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\demo-scenario.ps1 -Scenario worker-stall
```

## Evidence Utama

- mode demo berubah ke `worker_stall`
- queue backlog tinggi
- worker tidak sehat
- dokumen backlog/worker stall ikut diretrieval

## Candidate Action yang Diharapkan

- `restart_demo_worker`
- `retry_demo_background_job`
- `pause_demo_queue_consumer`

## Policy Outcome yang Diharapkan

- action medium-risk `pause_demo_queue_consumer` tetap `approval_required`
- policy menampilkan dependency awareness dan rollback plan

## Verification Outcome yang Diharapkan

- action medium-risk dijalankan
- verification status `failed`
- rollback `resume_demo_queue_consumer` dipicu otomatis
- incident pindah ke `rolled_back`

## Yang Perlu Ditunjukkan Saat Demo

- risk level medium-risk
- approval flow
- rollback records
- verification evidence yang menyatakan escalation tidak lagi direkomendasikan setelah rollback sukses
