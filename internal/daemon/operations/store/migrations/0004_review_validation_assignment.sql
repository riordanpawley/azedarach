-- Authority: project.daemon_operations.
-- Schema: expand durable validation requests with the assigned reviewer and
-- exact review-request observation event that authorized the candidate gate.
-- Data: existing validation rows remain unassigned (empty reviewer, epoch 0)
-- and therefore cannot authorize an active-validation review return.
-- Validation: startup verifies both non-null columns, defaults, and the
-- non-negative epoch constraint, failing closed on applied-ledger drift.
-- Ledger: the shared migration runner records this artifact ID and pinned
-- SHA-256 exactly once in schema_migrations within the schema transaction.

ALTER TABLE daemon_validation_requests
    ADD COLUMN reviewer_id TEXT NOT NULL DEFAULT '';

ALTER TABLE daemon_validation_requests
    ADD COLUMN review_epoch_event_id INTEGER NOT NULL DEFAULT 0
    CHECK (review_epoch_event_id >= 0);
