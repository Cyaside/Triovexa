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

Minimal siapkan:

- PostgreSQL server lokal
- database bernama `triovexa`
- user yang punya izin create table dan write data

## Strategi Development

- aplikasi melakukan migrasi schema dasar saat startup
- test HTTP dan orchestration tidak wajib memakai PostgreSQL nyata setiap saat
- runtime utama tetap diasumsikan memakai PostgreSQL

## Catatan

- `prd.md` dan `guide/` sudah diselaraskan secara lokal agar storage baseline memakai PostgreSQL
- bila belum ada PostgreSQL lokal, aplikasi server utama tidak akan bisa start sampai `DATABASE_URL` valid tersedia
