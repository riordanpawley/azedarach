# dec-8: Keep SQLite WAL enabled with safe local-first maintenance

- Created: 2026-07-09
- Updated: 2026-07-09

## Rationale

The Chefy incident showed that disabling WAL would trade away the local concurrency SQLite gives Azedarach, while SQLite's documented WAL model expects applications to bound WAL growth with checkpoints and avoid long-lived readers. Azedarach should keep WAL mode enabled, add size-aware PASSIVE checkpoint maintenance, reserve TRUNCATE for explicit/idle maintenance, expose checkpoint diagnostics, and retry only safe idempotent writes on transient SQLITE_BUSY or SQLITE_BUSY_SNAPSHOT.

## Context

Issue cxz hardens local project DB behavior after a 2.2G WAL and transient lock failures. Research sources are captured in docs/23-sqlite-wal-policy.md and the linked requirement req-sqlite-local-first-wal.

## Consequences

CLI/TUI stay thin clients and request WAL diagnostics through daemon protocol. Operators get az issue doctor --checkpoint-wal and --truncate-wal. Future write retry additions must prove operation-level idempotence and avoid retrying side effects that may already have partially committed.

## Links

- applies-to issue:cxz
- applies-to requirement:req-sqlite-local-first-wal
