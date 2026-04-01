# Policy Rules v1

Dokumen ini menjelaskan rule baseline sebelum policy engine menjadi lebih kompleks.

## Rule Inti

1. Action di luar catalog otomatis ditolak.
2. Kill switch global memblok seluruh write path.
3. Action hanya boleh dijalankan pada environment yang diizinkan.
4. Action hanya boleh dijalankan pada target yang diizinkan.
5. Semua low-risk action pada fase awal tetap approval-gated.
6. Medium-risk action hanya boleh lanjut ke approval bila target punya dependency metadata dan rollback plan yang jelas.
7. Medium-risk action tetap dibatasi environment, cooldown, dan max attempts.
8. High-risk action diblok pada MVP dan foundation phase.

## Kenapa Rule Ini Dipilih

- menjaga AI tetap constrained
- membuat approval flow tetap berarti
- mencegah execution liar sebelum verification siap
- meminimalkan risiko pada demo dan local-first development
- memastikan medium-risk action tidak aktif tanpa compensation path
- memberi konteks dependency dasar sebelum operator mengeksekusi action yang lebih kuat

## Evolusi Yang Direncanakan

- medium-risk action tertentu sudah bisa `approval_required` bila safety controls lengkap
- beberapa low-risk action dapat menjadi auto-runnable setelah verification dan safety controls matang
- rule sekarang sudah memakai dependency awareness, cooldown, dan max attempts dasar
- evaluasi berikutnya fokus pada approval scope yang lebih granular dan dependency blast-radius yang lebih detail
