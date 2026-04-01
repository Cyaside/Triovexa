# Scenario: Timeout After Deploy

## Tujuan

Menunjukkan alur triage sampai verification sukses untuk action low-risk `refresh_demo_cache`.

## Trigger

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\demo-scenario.ps1 -Scenario timeout-after-deploy
```

## Evidence Utama

- mode demo berubah ke `timeout_after_deploy`
- latency dan error rate meningkat
- deploy context baru muncul
- runbook dan postmortem timeout after deploy ikut diretrieval

## Candidate Action yang Diharapkan

- `refresh_demo_cache`

## Policy Outcome yang Diharapkan

- `approval_required`

## Verification Outcome yang Diharapkan

- action dieksekusi
- verification status `success`
- incident pindah ke `resolved`

## Yang Perlu Ditunjukkan Saat Demo

- evidence deploy dan metric
- rationale kenapa cache refresh dipilih
- execution record
- verification result
- counter internal di `/metrics`
