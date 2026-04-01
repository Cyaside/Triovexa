# Testing Strategy

Dokumen ini menyelaraskan testing Triovexa dengan PRD section 31. Tujuannya bukan hanya memastikan flow bekerja, tetapi juga memastikan safety guardrails tidak mudah bocor.

## Prinsip

- prioritaskan flow end-to-end yang mencerminkan lifecycle incident nyata
- pastikan safety rule diuji sama seriusnya dengan success path
- gunakan demo adapter dan in-memory repository untuk menjaga test tetap cepat
- simpan failure mode penting dalam test agar regression mudah terdeteksi

## Functional Testing

Yang sudah diotomasi:

- webhook intake
- incident creation
- triage generation
- candidate action generation
- policy decision
- approval workflow
- execution workflow
- verification workflow
- rollback workflow

Referensi utama:

- `internal/http/server_test.go`
- `internal/approval/service_test.go`
- `internal/execution/service_test.go`
- `internal/verification/service_test.go`

## Integration Testing

Yang sudah diotomasi:

- Grafana-compatible webhook -> incident intake -> triage -> candidate actions
- approval -> execution -> verification -> incident state transition
- medium-risk execution -> verification failure -> rollback -> rolled_back state
- metrics dan diagnostics endpoint setelah workflow berjalan

Karena mode demo masih local-first, integrasi ini menggunakan:

- `httptest.Server`
- `MemoryStore`
- demo HTTP adapter

## Safety Testing

Yang sudah diotomasi:

- action yang tidak ada di catalog ditolak oleh policy
- action tanpa approval tidak bisa dieksekusi
- kill switch memblokir execution
- duplicate execution dicegah oleh idempotency guard
- max attempt dan cooldown dijaga di execution layer
- execution scope direvalidasi sebelum adapter dipanggil
- missing action endpoint sekarang mengembalikan `404`

## Failure Testing

Yang sudah diotomasi:

- retryable adapter error pada execution
- verification `failed`
- verification `inconclusive`
- rollback sukses setelah verification gagal
- rollback gagal tetap mengarah ke escalation recommendation

Yang masih bisa ditambah pada iterasi berikutnya:

- PostgreSQL integration test nyata dengan container
- collector failure dari observability source sungguhan
- AI provider timeout saat provider eksternal sudah diaktifkan
- webhook secret validation failure
- authorization failure untuk endpoint approval/execution

## Cara Menjalankan

Semua test:

```powershell
go test ./...
```

Static analysis ringan:

```powershell
go vet ./...
```

Package spesifik yang sering disentuh:

```powershell
go test ./internal/http ./internal/approval ./internal/execution ./internal/verification
```

## Exit Criteria Untuk Demo-Ready

Sebelum presentasi atau recording demo:

- `go test ./...` harus hijau
- `go vet ./...` harus hijau
- scenario `timeout-after-deploy` harus bisa berakhir di `resolved`
- scenario `worker-stall` harus bisa menunjukkan rollback sukses
- `/metrics`, `/debug/tools`, dan `/debug/policies` harus bisa diakses
