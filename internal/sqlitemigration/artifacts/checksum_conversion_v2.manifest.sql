-- Migration checksum conversion format v2.
--
-- Compatibility effect: preserve and validate every executed v1 authority
-- marker and its immutable f8782f... artifact identity. V2 never rewrites or
-- deletes v1 rows.
--
-- Schema effect: retain schema_migration_checksum_conversions, keyed by stable
-- migration authority and conversion format version. The v2 marker records the
-- pinned checksum of this immutable manifest.
--
-- Data effect: an authority with no earlier conversion marker may backfill blank
-- artifact_checksum values for known applied IDs exactly once. An authority with
-- a valid v1 marker validates existing checksums without another legacy backfill.
-- Unknown migration IDs remain unchanged.
--
-- Validation effect: reject known blank or mismatched migration checksums after
-- any completed format; reject blank or mismatched known marker checksums,
-- marker-table schema drift, and unexpected marker-table triggers. Marker INSERT
-- must affect exactly one row and the pinned checksum is re-read before commit.
--
-- Ledger effect: insert the v2 marker only after every known applied migration
-- row and every historical marker have been verified. All v2 effects commit in
-- one BEGIN IMMEDIATE transaction.
CREATE TABLE IF NOT EXISTS schema_migration_checksum_conversions (
    authority_id TEXT NOT NULL,
    format_version INTEGER NOT NULL,
    artifact_checksum TEXT NOT NULL,
    completed_at TEXT NOT NULL,
    PRIMARY KEY(authority_id, format_version),
    CHECK(trim(authority_id) <> ''),
    CHECK(format_version > 0),
    CHECK(trim(artifact_checksum) <> '')
);
