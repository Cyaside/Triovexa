# Scenario: Error Rate Spike

## Tujuan

Menunjukkan skenario ringan yang tetap menghasilkan triage dan candidate action terkontrol.

## Trigger

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\demo-scenario.ps1 -Scenario error-rate-spike
```

## Evidence Utama

- mode demo berubah ke `error_rate_spike`
- error rate dan latency naik
- tidak ada rollback workflow yang dipakai

## Candidate Action yang Diharapkan

- `refresh_demo_cache` bila latency cukup tinggi

## Policy Outcome yang Diharapkan

- `approval_required`

## Verification Outcome yang Diharapkan

- tergantung action yang dijalankan, tetapi flow approval, execution, dan audit trail tetap bisa ditunjukkan

## Yang Perlu Ditunjukkan Saat Demo

- triage tetap evidence-based walau skenario lebih sederhana
- policy engine tetap membatasi action
- audit trail tetap lengkap
