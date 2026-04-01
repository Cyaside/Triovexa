# Worker Stall With Rollback

## Ringkasan

Insiden backlog tinggi pada consumer checkout pernah memburuk saat consumer dipause terlalu lama. Setelah itu rollback cepat dengan resume consumer terbukti menjadi guardrail yang aman.

## Pelajaran Utama

- medium-risk action tetap perlu approval operator
- rollback plan harus eksplisit sebelum action dijalankan
- verification harus cepat dan evidence-based

## Relevansi Untuk Triovexa

Dokumen ini dipakai untuk memperkuat alasan kenapa `pause_demo_queue_consumer` tidak boleh berjalan tanpa rollback plan dan kenapa automatic rollback perlu diprioritaskan bila outcome awal buruk.
