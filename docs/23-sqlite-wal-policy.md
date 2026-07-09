# SQLite WAL Lifecycle Policy

## Research Notes

SQLite WAL mode is still the right default for Azedarach local project databases. The SQLite WAL documentation describes the core tradeoff: readers and writers can proceed concurrently, but checkpointing is what moves frames back into the database file and keeps WAL growth bounded. SQLite's default auto-checkpoint threshold is 1000 pages, and applications that defer checkpointing can let WAL files grow excessively. See: https://sqlite.org/wal.html

SQLite auto-checkpoints are passive. The `sqlite3_wal_autocheckpoint` documentation states that auto-checkpoints run after commits when the WAL reaches the configured frame threshold, and the checkpoints initiated by that mechanism are `PASSIVE`. Passive checkpoints avoid blocking writers/readers but may leave frames behind when readers hold old snapshots. See: https://sqlite.org/c3ref/wal_autocheckpoint.html

`PRAGMA wal_checkpoint` exposes the operational tuple Azedarach needs for support diagnostics: whether the checkpoint was busy, how many frames were in the log, and how many were checkpointed. `TRUNCATE` is stronger operational cleanup and should be explicit or reserved for known-idle windows because active readers can prevent completion. See: https://sqlite.org/pragma.html#pragma_wal_checkpoint

Busy handling is necessary but not sufficient. `busy_timeout` lets SQLite wait on locks, but callers still need operation-level retry around safe, idempotent writes because transient busy and busy-snapshot results can escape after the timeout or from snapshot upgrade conflicts. See: https://sqlite.org/pragma.html#pragma_busy_timeout and https://sqlite.org/rescode.html#busy_snapshot

## Azedarach Policy

- Keep `journal_mode=WAL` and `synchronous=NORMAL` for project issue databases.
- Keep local reads short-lived: close `Rows` promptly, avoid holding read transactions across watch loops, and do not stream subscription loops from one open SQLite snapshot.
- Serialize in-process taskstore writes with the existing per-database write/mutation locks.
- Retry only safe operation-level writes on transient SQLite busy results. Do not blindly retry operations that may perform non-idempotent side effects outside the row being updated.
- Run automatic `PASSIVE` WAL maintenance after successful writes on a bounded cadence when the WAL exceeds the policy threshold.
- Log WAL size, checkpoint mode, busy result, frame counts, and before/after WAL bytes for attribution.
- Use `TRUNCATE` only through explicit maintenance commands or known-idle/emergency cleanup paths.
- Use `az issue doctor --checkpoint-wal <issue-id>` for safe passive maintenance and `az issue doctor --truncate-wal <issue-id>` for explicit cleanup after confirming the project is idle enough for aggressive checkpointing.
