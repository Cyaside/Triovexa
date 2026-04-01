# Postmortem: Error Rate Spike Setelah Release

## Ringkasan

Rilis service demo menyebabkan error rate meningkat pada worker dan API secara bersamaan. Incident cepat memburuk karena operator butuh waktu untuk mengumpulkan logs, metrics, dan deploy context secara manual.

## Dugaan Penyebab

- perubahan rilis belum kompatibel dengan job payload lama
- worker crash loop setelah menerima payload tertentu
- cache state tidak sinkron setelah release

## Evidence Yang Biasanya Terlihat

- spike error rate lintas beberapa komponen
- worker restart berulang
- antrean job menumpuk

## Pelajaran

- runbook restart worker dan retry job sangat penting sebagai initial response
- evidence, inference, dan action harus dipisahkan dengan jelas
- approval-gated remediation membantu mengurangi tindakan panik
