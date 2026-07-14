---
name: database-migration-review
description: Review, consolidate, test, and harden Azedarach SQLite migrations before integration. Use for any migration, schema ensure/repair logic, trigger or index change, persistence-authority change, migration failure, or pre-merge review of a branch containing database changes. Also use when validating upgrades against real root-user or registered project databases.
---

# Database Migration Review

Treat migrations as release-critical persistence code. Preserve user data, immutable history, and a reproducible upgrade path.

## Pre-merge shape gate

1. Diff the merge base against the integration head, including migration registries and embedded SQL.
2. Treat every migration present on the merge base or recorded by a shared/user database as immutable.
3. Require exactly one embedded artifact for every registered ID and verify its pinned SHA-256. Reject callback-only registrations, duplicate IDs, missing/empty artifacts, registry/artifact mismatches, and any historical checksum change.
4. For Go-assisted migrations, inspect the SQL/manifest artifact for explicit schema, data, validation, and ledger effects. Go may provide transactional orchestration or data-dependent execution only; it must not be the sole migration record.
5. Default to exactly one new migration for the entire merge to main. Consolidate all branch-local, unmerged migration steps into one coherent forward migration and one ledger ID before clone testing or integration.
6. Do not renumber or squash migrations already merged to main or plausibly executed against any real database, including a developer database from the branch. Once executed outside a disposable fixture/clone, treat the migration as immutable and repair it forward.
7. Allow more than one new migration only when independently versioned database authorities are both changed, or when a real intermediate commit/transaction boundary is required for correctness or compatibility. Still consolidate to one migration per affected authority where possible. Record why further consolidation is unsafe in closure evidence; convenience, separate worker ownership, or incremental development is not sufficient.
8. Re-run the base-versus-head diff after consolidation and reject mutated historical migrations, duplicate IDs, gaps caused by renumbering, or dead branch-local migrations.

## Design gate

- Inspect the previous production ledger, tables, columns, constraints, triggers, indexes, and representative row products before designing the migration.
- Prefer expand-and-contract when old and new binaries may overlap. Add and backfill authority before removing legacy readers, writers, columns, or adapters.
- Keep schema change, data normalization, derived-projection cleanup, and ledger recording in the intended transaction boundary.
- Make retries and already-migrated opens safe. Record the ledger entry on both mutation and verified no-op paths.
- Drop or replace obsolete triggers before backfills they would intercept; reinstall canonical guards only after validation.
- Never make manual production SQL edits the durable repair. Ship a new forward migration and prove it against a clone of the broken shape.

## Required validation matrix

Exercise the real store/startup/client path, not only isolated SQL:

1. Fresh empty database to latest.
2. Faithful immediately previous production fixture to latest.
3. Latest database reopened as an idempotent no-op.
4. Injected failure proving atomic rollback and old-data usability.
5. Relevant schema/ledger drift, including an applied ID with missing columns, triggers, indexes, backfills, or markers.
6. Copies of every actual local database in scope:
   - root user database at `~/.azedarach/azedarach.db`
   - every registered project database, normally `<project>/.azedarach/azedarach.db`

## Clone real databases safely

Never run a candidate migration against the original database during review.

1. Enumerate registered projects from the production registry/config and include the root user database separately; they use different migration authorities.
2. Create a consistent temporary clone. Prefer SQLite's online backup API/command while the daemon is live. Otherwise stop the daemon cleanly before copying the database together with any required WAL state. Do not use a plain live-file copy that can omit committed WAL pages.
3. Keep clones outside registered project paths so production discovery cannot open them.
4. Run the candidate binary's real database-open/startup path sequentially against each clone. Prevent hooks or a global daemon from auto-starting against clone or source paths.
5. Reopen every migrated clone and run active reads used by CLI/TUI/daemon startup.
6. Delete only temporary artifacts created by this review. Never alter or replace source databases.

If real databases contain private content, keep clones local, restrict permissions, and do not attach or publish them.

## Assertions

For every fixture and clone, assert:

- expected ledger IDs/checksums appear exactly once
- every applied known ID has a non-empty `artifact_checksum` equal to the pinned artifact SHA-256
- expected columns, constraints, triggers, and indexes exist
- deprecated schema objects are absent only when contraction is authorized
- row counts, IDs, timestamps, archive/terminal state, and user-authored data are preserved
- canonical backfills and normalization produce only valid domain state products
- stale derived projections or claims are removed only when their source authority is absent or ineligible
- a second open performs no mutation and succeeds
- representative CLI/TUI/daemon reads succeed within their normal timeout

Compare source and migrated-clone summaries explicitly; a successful open alone is insufficient.

## Review convergence and evidence

Run three clean review passes after the last migration-affecting edit:

1. Merge shape and immutable-history pass, including the one-migration consolidation gate.
2. Upgrade, rollback, idempotency, and compatibility pass.
3. Real-database clone and active-path operational pass.

Reset the clean-pass count after any executable or test change. Before integration, record:

- merge base and migration diff
- new migration count and consolidation decision
- databases/fixtures cloned, without exposing private contents
- commands and schema/data assertions
- rollback and second-open results
- full-suite result and compatibility window
- any justified exception to one migration per merge

Fresh-database-only evidence is never sufficient for merge.
