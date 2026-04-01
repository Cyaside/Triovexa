# Pause Demo Queue Consumer

## Kapan Dipakai

Gunakan hanya ketika queue backlog terus meningkat dan worker downstream tidak mampu mengejar laju pesan baru.

## Safety Notes

- ini adalah action medium-risk
- wajib approval operator
- hanya dipakai pada target `demo-queue-consumer`
- rollback yang disiapkan adalah `resume_demo_queue_consumer`

## Langkah

1. pastikan backlog benar-benar terus naik
2. konfirmasi dependency map dan downstream yang dilindungi
3. approve action hanya jika rollback plan siap
4. jalankan verification segera setelah eksekusi

## Success Signal

- blast radius berhenti meluas
- backlog tidak naik secepat sebelumnya
- operator punya waktu untuk stabilisasi
