# Immutable Migration Artifacts

Every SQLite migration ledger ID maps to exactly one embedded, committed artifact. Ordinary migrations use their executable `.sql` file. Go-assisted migrations use either that SQL file or a `.manifest.sql` file that states the schema, data, validation, and ledger effects while Go remains transactional orchestration only.

Each authority keeps a pinned SHA-256 catalog beside its registry. Startup validates the registry before migration work and refuses duplicate IDs, missing or empty artifacts, catalog mismatches, and changed artifact content. `schema_migrations.artifact_checksum` records the pinned checksum in the same transaction and insert as every newly applied migration marker. Existing ledgers are upgraded compatibly: adding the checksum column and backfilling all known historical rows is one retry-safe transaction. Only rows that predate checksum support may be backfilled; a later blank or mismatched checksum fails startup. Unknown ledger IDs remain untouched for forward compatibility.

Historical IDs, artifacts, and checksums are immutable after merge or execution. Correct mistakes with a new forward migration; never edit the old artifact or update its pinned checksum. A new Go-assisted migration is incomplete until its descriptive artifact is committed and registered.

Pre-merge review follows `.codex/skills/database-migration-review/SKILL.md`: compare base and head registries/artifacts, verify checksums and one-artifact-per-ID coverage, exercise fresh/historical/rollback/drift/idempotent paths, and run candidate startup against safe online-backup clones of the root user database and every registered project database.
