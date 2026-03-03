# ADR-003: Error Taxonomy

## Status
Accepted

## Context
Error handling is inconsistent across service boundaries and UI overlays, making routing (toast vs overlay vs retry) hard to automate.

## Decision
- Classify errors into: user-actionable, transient-infrastructure, and programmer-bug.
- Attach stable error codes where possible.
- Route by class: actionable -> focused overlay, transient -> retry/toast, bug -> fail-fast logging.

## Consequences
- Better operator UX with consistent remediation paths.
- Probe operation summary can carry stable `errorCode`.
- Existing call sites need wrapping to preserve taxonomy.
