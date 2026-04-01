# Incident State Machine

State machine ini menjadi sumber aturan lifecycle incident agar alur sistem tetap terkendali dan dapat diaudit.

## Daftar State

- `detected`
- `triaging`
- `action_proposed`
- `awaiting_approval`
- `approved`
- `executing_action`
- `verifying_action`
- `resolved`
- `failed_remediation`
- `rolled_back`
- `escalated`
- `closed`

## Transisi Utama

- `detected -> triaging`
- `triaging -> action_proposed`
- `action_proposed -> awaiting_approval`
- `action_proposed -> approved`
- `approved -> executing_action`
- `executing_action -> verifying_action`
- `verifying_action -> resolved`
- `verifying_action -> failed_remediation`
- `failed_remediation -> rolled_back`
- `failed_remediation -> escalated`
- `resolved -> closed`
- `escalated -> closed`

## Prinsip

- incident tidak boleh lompat langsung dari proposal ke execution tanpa approval atau decision policy
- verification adalah state eksplisit, bukan sekadar side effect
- rollback dan escalation harus terlihat sebagai perubahan state nyata
- close hanya boleh terjadi dari state terminal yang jelas
