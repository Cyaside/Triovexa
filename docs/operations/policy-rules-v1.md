# Policy Rules v1

Dokumen ini menjelaskan rule baseline sebelum policy engine menjadi lebih kompleks.

## Rule Inti

1. Action di luar catalog otomatis ditolak.
2. Kill switch global memblok seluruh write path.
3. Action hanya boleh dijalankan pada environment yang diizinkan.
4. Action hanya boleh dijalankan pada target yang diizinkan.
5. Semua low-risk action pada fase awal tetap approval-gated.
6. Medium-risk action diblok sampai phase berikutnya benar-benar siap.
7. High-risk action diblok pada MVP dan foundation phase.

## Kenapa Rule Ini Dipilih

- menjaga AI tetap constrained
- membuat approval flow tetap berarti
- mencegah execution liar sebelum verification siap
- meminimalkan risiko pada demo dan local-first development

## Evolusi Yang Direncanakan

- medium-risk action akan beralih dari `deny` ke `approval_required` di phase lanjut
- beberapa low-risk action dapat menjadi auto-runnable setelah verification dan safety controls matang
- rule akan diperkaya dengan dependency awareness, cooldown, dan max attempts
