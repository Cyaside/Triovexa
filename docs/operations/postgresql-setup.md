# PostgreSQL Setup

Dokumen ini menjelaskan baseline persistence yang dipakai proyek setelah keputusan arsitektur digeser dari SQLite ke PostgreSQL.

## Kenapa PostgreSQL

- lebih dekat ke arsitektur production-like
- skema incident, audit, dan triage lebih realistis diuji pada database server-based
- mengurangi kebutuhan migrasi storage besar di fase menengah

## Konfigurasi Minimum

Server memakai `DATABASE_URL` sebagai sumber konfigurasi utama. Contoh:

```env
DATABASE_URL=postgres://postgres:postgres@localhost:5432/triovexa?sslmode=disable
```

## Kebutuhan Lokal

Opsi termudah sekarang adalah memakai otomasi Docker Compose yang sudah ada di repo.

## Cara Menjalankan Otomasi Lokal

1. Pastikan Docker Desktop aktif.
2. Jalankan:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev-up.ps1
```

Atau jika ingin langsung menyalakan database, demo service, dan app server sekaligus:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev-start.ps1
```

3. Untuk melihat status:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev-status.ps1
```

4. Untuk mematikan environment lokal:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev-down.ps1
```

Otomasi ini akan menyalakan PostgreSQL lokal dengan konfigurasi berikut:

- database: `triovexa`
- user: `postgres`
- password: `postgres`
- port: `5432`
- syarat: Docker daemon harus sedang berjalan dan dapat diakses dari terminal
- `dev-start.ps1` akan membuka dua jendela PowerShell tambahan untuk service lokal

## Strategi Development

- aplikasi melakukan migrasi schema dasar saat startup
- test HTTP dan orchestration tidak wajib memakai PostgreSQL nyata setiap saat
- runtime utama tetap diasumsikan memakai PostgreSQL
- local bootstrap diotomasi lewat `docker-compose.yml` dan skrip PowerShell

## Catatan

- `prd.md` dan `guide/` sudah diselaraskan secara lokal agar storage baseline memakai PostgreSQL
- bila belum ada PostgreSQL lokal, aplikasi server utama tidak akan bisa start sampai `DATABASE_URL` valid tersedia
- bila image `postgres:17-alpine` belum ada, Docker akan menarik image tersebut saat pertama kali setup
- bila Docker daemon belum aktif, `dev-up.ps1` dan `dev-start.ps1` akan berhenti lebih awal dengan pesan error yang jelas
