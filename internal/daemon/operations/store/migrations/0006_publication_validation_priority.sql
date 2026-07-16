-- Authority: project.daemon_operations.
-- Schema: narrow aggregate exclusivity to controlled timing capacity so
-- publication push/review validation can start immediately and concurrently.
-- Data: no validation request rows are rewritten or removed.
-- Validation: startup verifies the capacity-only partial unique index.
-- Ledger: the shared migration runner records this immutable artifact and its
-- pinned SHA-256 exactly once in the same transaction as the index change.

DROP INDEX idx_daemon_validation_one_active_aggregate;

CREATE UNIQUE INDEX idx_daemon_validation_one_active_aggregate
    ON daemon_validation_requests(project_id)
    WHERE state = 'active' AND class = 'aggregate' AND purpose = 'capacity';
