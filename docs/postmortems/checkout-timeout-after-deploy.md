# Postmortem: Checkout Timeout Setelah Deploy

## Ringkasan

Setelah deploy versi baru `checkout-service`, error timeout meningkat tajam dalam 7 menit pertama. Alert berasal dari lonjakan error rate dan peningkatan latency pada endpoint checkout utama.

## Dugaan Penyebab

- koneksi ke dependency pembayaran menjadi lebih lambat
- konfigurasi timeout internal terlalu agresif
- perubahan deploy meningkatkan jumlah retry yang tidak terkendali

## Evidence Yang Biasanya Terlihat

- spike error rate segera setelah deploy
- log timeout pada integration layer
- latency P95 dan P99 meningkat

## Pelajaran

- deploy context harus selalu ikut diambil saat triage
- rollback atau feature flag rollback perlu tersedia untuk kasus produksi
- action awal harus dibatasi dan melalui approval
