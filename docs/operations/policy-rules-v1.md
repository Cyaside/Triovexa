# Policy Rules v1

This document describes the baseline rules enforced by the current policy engine.

## Core Rules

1. Actions outside the catalog are automatically denied.
2. The global kill switch blocks the full write path.
3. An action may only run in allowed environments.
4. An action may only run against allowed targets.
5. Low-risk actions remain approval-gated.
6. Medium-risk actions only proceed to approval when the target has dependency metadata and a clear rollback plan.
7. Medium-risk actions remain constrained by environment, cooldown, and max attempts.
8. High-risk actions are blocked.

## Why These Rules Exist

- keep the AI layer constrained
- preserve the meaning of the approval flow
- prevent uncontrolled execution before verification is ready
- minimize risk during demos and local-first development
- ensure medium-risk actions never run without a compensation path
- provide dependency context before operators execute stronger actions

## Planned Evolution

- some medium-risk actions may eventually move into a more nuanced approval policy when safety controls mature
- selected low-risk actions may become auto-runnable once verification and safety controls are stronger
- the current rules already include dependency awareness, cooldowns, and max attempts
- future refinement should focus on granular approval scopes and more detailed dependency blast-radius modeling
