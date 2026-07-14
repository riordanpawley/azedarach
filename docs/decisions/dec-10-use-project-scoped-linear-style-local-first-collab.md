# dec-10: Use project-scoped Linear-style local-first collaboration

- Created: 2026-07-12
- Updated: 2026-07-14

## Rationale

Use a complete local SQLite projection plus a durable pending-command outbox on every client. In team mode, one self-hosted single-active project service backed by SQLite WAL validates commands and assigns canonical order; clients never become peer authorities for guarded operations. Ordinary content commands may be queued offline, while guarded commands require current server validation. Detailed canonical event, cursor, projection, replay, signing, and recovery mechanics are owned by companion investigation dgv rather than this product-level decision.

## Context

Azedarach is primarily an AI context and durable history manager for a small developer team. Users want Linear-like instantaneous local reads and offline content work, but lifecycle, graph, review, integration, and membership invariants need one online ordering and validation authority. Execution and orchestration remain local and outside collaboration v1 under dec-16.

## Consequences

Self-hosting is the first deployment promise and Docker Compose is the first-run path. One serialized SQLite writer is sufficient for the one-project small-team topology; PostgreSQL is reconsidered for multi-instance HA, consolidated multi-project hosting, or sustained write contention. Client-creatable entities need immutable UUIDv7 identities plus server-assigned human display keys. Field patches use server acceptance order, disjoint fields coexist, superseded values remain inspectable, and stale guarded commands may reject. Solo-to-team promotion imports one owner-selected database as genesis; other local stores are not silently merged.

## Links

- applies-to issue:dda
- applies-to issue:dgv — Local-first collaboration and single-active SQLite authority are fixed product inputs.
