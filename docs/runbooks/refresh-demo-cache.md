# Runbook: Refresh Demo Cache

## Tujuan

Memulihkan cache non-critical yang stale atau korup ringan tanpa memengaruhi jalur kritikal.

## Kapan Digunakan

- response masih menggunakan data usang
- cache miss atau cache corruption ringan terdeteksi
- refresh aman dilakukan pada service demo

## Langkah Manual

1. Verifikasi masalah memang berasal dari layer cache.
2. Identifikasi cache key spesifik jika tersedia.
3. Jalankan refresh cache untuk target yang aman.
4. Pantau latency dan error rate setelah refresh.

## Sinyal Keberhasilan

- data baru terambil dengan benar
- latency tidak memburuk
- error tidak meningkat
