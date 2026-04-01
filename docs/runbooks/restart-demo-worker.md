# Runbook: Restart Demo Worker

## Tujuan

Memulihkan worker demo non-critical yang berhenti memproses job atau mengalami error sementara.

## Kapan Digunakan

- queue backlog meningkat
- worker tidak mengambil job baru
- error worker meningkat tanpa indikasi kerusakan data

## Langkah Manual

1. Pastikan insiden hanya berdampak pada worker non-critical.
2. Cek apakah ada deploy baru yang berhubungan dengan worker.
3. Verifikasi log worker menunjukkan error sementara atau crash berulang.
4. Restart worker target.
5. Pantau backlog, error rate, dan health signal.

## Sinyal Keberhasilan

- backlog mulai turun
- worker kembali memproses job
- error log mereda
