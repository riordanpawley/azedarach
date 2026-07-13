-- Migration checksum conversion format v1.
--
-- Schema effect: create schema_migration_checksum_conversions, keyed by the
-- stable migration authority and conversion format version. The recorded
-- artifact_checksum identifies this immutable manifest.
--
-- Data effect: for an authority without a completed v1 marker, populate blank
-- artifact_checksum values only for applied migration IDs in that authority's
-- pinned embedded catalog. Unknown migration IDs are preserved unchanged.
--
-- Validation effect: reject nonblank historical checksum mismatches before or
-- after conversion; after a v1 marker exists, also reject blank checksums for
-- known applied IDs and reject blank or mismatched marker checksums.
--
-- Ledger effect: insert the v1 authority marker only after every known applied
-- migration row has been re-read and verified. Schema, data, validation, and
-- marker changes commit in one BEGIN IMMEDIATE transaction.
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
