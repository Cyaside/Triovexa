# High-Level Architecture

Dokumen ini menerjemahkan arsitektur produk pada PRD ke fondasi implementasi awal.

## Komponen Utama

1. `Alert Receiver`
   Menerima alert webhook dari Grafana dan menormalisasi payload.

2. `Incident Orchestrator`
   Mengelola lifecycle incident dari `detected` sampai `closed`.

3. `Context Collectors`
   Mengambil logs, metrics, deploy info, dan metadata layanan.

4. `Knowledge Retriever`
   Mengambil runbook, postmortem, dan dokumen operasional yang relevan.

5. `AI Agent Layer`
   Menyusun summary, hypotheses, next steps, dan candidate actions berbasis evidence.

6. `Policy Engine`
   Memutuskan apakah candidate action diizinkan, perlu approval, atau ditolak.

7. `Approval Layer`
   Menjadi gerbang human-in-the-loop untuk action tertentu.

8. `Execution Engine`
   Menjalankan action yang telah lolos policy dan approval.

9. `Verification Engine`
   Memeriksa apakah action memperbaiki kondisi.

10. `Persistence & Audit Store`
    Menyimpan incident, triage result, policy decision, execution record, verification result, dan audit event.

11. `Operator UI`
    Menampilkan incident list, incident detail, evidence, action, approval, dan audit trail.

## Alur Tingkat Tinggi

```text
Grafana Alert
  -> Alert Receiver
  -> Incident Orchestrator
     -> Context Collectors
     -> Knowledge Retriever
     -> AI Agent
     -> Policy Engine
     -> Approval Layer
     -> Execution Engine
     -> Verification Engine
  -> Persistence & Audit Store
  -> Operator UI
```

## Boundary Awal

- vendor-specific integration harus hidup di adapter masing-masing
- orchestration core tidak boleh tahu detail implementasi vendor
- AI provider harus bisa diganti tanpa merusak domain incident
- write path hanya boleh lewat execution adapter yang terkontrol
- policy decision dan approval harus selalu persisten

## Implementasi Yang Sudah Disiapkan di Phase 00

- bootstrap HTTP service
- core domain entities
- incident state machine
- action catalog awal
- policy evaluator awal
- example app configuration
- baseline persistence layer berbasis PostgreSQL
