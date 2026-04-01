# Runbook: Retry Demo Background Job

## Tujuan

Menjalankan ulang background job demo yang gagal karena error sementara.

## Kapan Digunakan

- job gagal karena timeout sesaat
- dependency eksternal sempat tidak tersedia
- retry aman dan tidak menyebabkan duplikasi berbahaya

## Langkah Manual

1. Identifikasi `job_id` yang gagal.
2. Pastikan job termasuk kategori aman untuk di-retry.
3. Cek apakah dependency yang sebelumnya gagal sudah pulih.
4. Jalankan retry untuk job tersebut.
5. Verifikasi job selesai dan tidak menambah error baru.

## Sinyal Keberhasilan

- job selesai tanpa error
- antrean job kembali normal
- tidak ada lonjakan error lanjutan
