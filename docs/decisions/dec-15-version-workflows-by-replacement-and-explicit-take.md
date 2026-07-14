# dec-15: Version workflows by replacement and explicit takeover

- Created: 2026-07-14
- Updated: 2026-07-14

## Rationale

Model every durable workflow/process-manager instance as an event-sourced aggregate pinned to an immutable definition version. New versions start new instances while existing instances finish under their old version. When an active instance cannot safely finish, one command terminates it in a known handed-off state and records a deterministic handoff payload/target; a post-commit runner submits a separate idempotent start command to the new target-version stream under the same correlation. Partial failure remains visible and retryable. Never mutate a running process reducer/state in place as the normal versioning mechanism.

## Context

Investigation dgv requires a reusable event-driven workflow engine while aligning with Greg Young process-manager guidance: processes span transactions, must terminate or expose intervention, should not wait forever, and should be split when too large.

## Consequences

Definitions and reducer code become read-only after production use and remain digest-pinned for replay. Large workflows split into smaller process managers; every wait has a bounded timeout/escalation and every instance reaches a known terminal or intervention state. Replay runs with publishers disconnected; only a new live signal may emit messages. Handoff never creates a two-stream atomic commit. Workflow tests use Given prior messages / When one message / Then public output messages rather than private state.

## Links

- applies-to issue:dgv — Current workflow versioning decision; replaces dec-14 in-place migration allowance.
- revises decision:dec-14 — Revises the earlier recommendation that allowed workflow.migrate to switch reducer versions inside one instance stream.
