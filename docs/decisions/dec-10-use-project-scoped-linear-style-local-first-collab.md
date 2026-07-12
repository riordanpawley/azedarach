# dec-10: Use project-scoped Linear-style local-first collaboration

- Created: 2026-07-12
- Updated: 2026-07-12

## Rationale

Use a complete local SQLite projection for reads plus a durable pending-command outbox for offline/optimistic mutations. A self-hosted project service backed by PostgreSQL validates commands, assigns canonical project order, appends shared semantic events, and streams cursor-based changes back to clients. Team-mode offline clients create pending commands rather than authoritative facts; only the service emits canonical shared events. Solo mode retains a local authority that emits canonical events so its history can later be imported. This avoids peer co-authority for leases and lifecycle invariants while retaining local-first UX.

## Context

Azedarach is primarily an AI context and durable history manager. Repository issue, spec, decision, learning, evidence, and orchestration management form the semantic structure around that history. The first collaboration audience is a small team of developers, not a multi-team enterprise organization. Users want Linear-like instantaneous local behavior and offline metadata editing without synchronizing raw agent transcripts.

## Consequences

Self-hosting is the first deployment promise; a managed service can later run the same protocol. PostgreSQL is the shared authority; SQLite remains the local projection/standalone store. Solo-to-team promotion publishes the full semantic history after a privacy preview and identity binding. Stable global entity/event IDs, actor attribution, schema versions, idempotency, cursor bootstrap, and import deduplication are required from the beginning. Ordinary field-level patches use server arrival order; disjoint fields naturally coexist, same-field supersession remains inspectable/restorable in history, and guarded lifecycle/graph/review/integration/execution commands revalidate current invariants and may reject.

## Links

- applies-to issue:dda
