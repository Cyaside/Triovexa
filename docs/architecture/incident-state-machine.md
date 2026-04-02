# Incident State Machine

This state machine defines the incident lifecycle so the workflow stays controlled and auditable.

## States

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

## Main Transitions

- `detected -> triaging`
- `triaging -> action_proposed`
- `action_proposed -> awaiting_approval`
- `action_proposed -> approved`
- `approved -> executing_action`
- `executing_action -> verifying_action`
- `verifying_action -> resolved`
- `verifying_action -> failed_remediation`
- `verifying_action -> escalated`
- `failed_remediation -> rolled_back`
- `failed_remediation -> escalated`
- `resolved -> closed`
- `escalated -> closed`

## Principles

- an incident must not jump from proposal directly to execution without a policy decision or approval
- verification is an explicit state, not a side effect
- rollback and escalation must appear as real state transitions
- closing is only allowed from explicit terminal states
