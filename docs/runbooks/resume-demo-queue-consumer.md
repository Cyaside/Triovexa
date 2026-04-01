# Resume Demo Queue Consumer

## Kapan Dipakai

Dipakai sebagai rollback untuk `pause_demo_queue_consumer` setelah verification menyatakan hasil pause memperburuk kondisi atau tidak membantu.

## Langkah

1. resume consumer
2. cek health worker
3. bandingkan backlog sebelum dan sesudah resume
4. pastikan incident status bergerak ke `rolled_back` bila rollback sukses

## Success Signal

- consumer tidak lagi paused
- error rate, latency, atau backlog kembali mendekati baseline
- operator bisa lanjut ke investigasi manual dengan sistem yang lebih stabil
