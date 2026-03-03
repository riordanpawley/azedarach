# ADR-005: Probe Schema Versioning

## Status
Accepted

## Context
E2E probes need a stable JSON contract for automation, but the UI model will evolve.

## Decision
- Define a `schemaVersion` field in every probe payload.
- Reject payloads that do not match the runtime schema major/minor contract.
- Add marshal/unmarshal validation helpers in `internal/probe`.

## Consequences
- Probe consumers can gate parsing on version.
- Breaking changes are explicit and auditable.
- Introduces maintenance discipline: bump version with contract changes.
