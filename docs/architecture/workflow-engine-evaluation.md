# Workflow Engine Evaluation

Catatan ini mendokumentasikan evaluasi awal apakah orkestrasi internal Triovexa masih cukup untuk Phase 6 atau sudah perlu workflow engine eksternal seperti Temporal.

## Keputusan Saat Ini

- keputusan: tetap memakai orchestration internal
- status: diterima untuk Phase 6
- evaluasi ulang: setelah Phase 7 atau saat action portfolio bertambah signifikan

## Kenapa Masih Cukup

- jumlah state incident masih terbatas dan eksplisit
- retry dan timeout masih sederhana dan bisa diaudit langsung dari service code
- rollback yang ada baru satu jalur medium-risk dengan compensation yang pendek
- belum ada kebutuhan resume workflow lintas proses atau lintas hari

## Sinyal Yang Akan Memicu Evaluasi Ulang

- retry policy mulai berbeda jauh antar action
- rollback membutuhkan multi-step compensation
- action chain mulai melibatkan lebih dari satu sistem eksternal
- operator butuh resume workflow setelah process crash atau deploy
- audit trail perlu menunjukkan branch workflow yang lebih kompleks daripada state machine sekarang

## Risiko Kalau Terlalu Cepat Pindah Workflow Engine

- kompleksitas operasional naik sebelum use case benar-benar matang
- debugging lokal jadi lebih lambat
- model domain yang belum stabil bisa ikut membengkak karena constraint tool

## Risiko Kalau Terlalu Lama Menunda

- orchestration code mulai sulit dibaca karena banyak branch retry dan compensation
- state transition makin tersebar
- rollback dan escalation bisa saling tumpang tindih bila action portfolio bertambah cepat

## Guardrail Sampai Evaluasi Ulang

- pertahankan satu service orchestration utama
- simpan audit event dan state transition secara eksplisit
- hindari menambah action multi-step tanpa rollback plan yang jelas
- dokumentasikan tiap workflow baru yang menambah compensation atau branching
