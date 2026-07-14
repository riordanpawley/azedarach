# dec-24: Keep accepted semantic history permanent and retire projects by sealing

- Created: 2026-07-14
- Updated: 2026-07-14

## Rationale

Preserve accepted semantic records for the project lifetime. Apply corrections and reversals through superseding facts, use archive only for visibility, and retire a project by sealing it read-only and exporting recovery material before any explicit operator-level storage deletion.

## Context

Shared agent context, attribution, auditability, and recovery depend on stable history. Redaction and payload-erasure machinery are intentionally deferred from this issue.

## Consequences

Identity is not reused after archive or retirement. Retirement revokes authority to mutate while retaining inspectable history and artifacts until the operator deliberately removes deployment storage.

## Links

- applies-to issue:dda
