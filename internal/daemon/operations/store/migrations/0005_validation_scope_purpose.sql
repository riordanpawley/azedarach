-- Authority: project.daemon_operations.
-- Schema: expand durable validation requests with explicit attribution scope
-- and authorization purpose; add an index for authoritative review evidence.
-- Data: existing rows are tagged ticket/legacy so they remain inspectable but
-- can never satisfy the explicit review_evidence purpose.
-- Validation: startup verifies columns, constraints, backfill, and index.
-- Ledger: the shared migration runner records this immutable artifact and its
-- pinned SHA-256 exactly once within the schema transaction.

ALTER TABLE daemon_validation_requests
    ADD COLUMN scope TEXT NOT NULL DEFAULT 'ticket'
    CHECK (scope IN ('repository','ticket'));

ALTER TABLE daemon_validation_requests
    ADD COLUMN purpose TEXT NOT NULL DEFAULT 'legacy'
    CHECK (purpose IN ('legacy','capacity','development','push_gate','review_evidence'));

CREATE INDEX idx_daemon_validation_review_evidence
    ON daemon_validation_requests(project_id, issue_id, source_revision, sequence)
    WHERE scope = 'ticket' AND purpose = 'review_evidence' AND class = 'aggregate';

ALTER TABLE daemon_validation_requests
    ADD COLUMN execution TEXT NOT NULL DEFAULT 'executed'
    CHECK (execution IN ('executed','joined','reused','skipped'));

ALTER TABLE daemon_validation_requests
    ADD COLUMN authoritative_request_id TEXT NOT NULL DEFAULT '';

ALTER TABLE daemon_validation_requests
    ADD COLUMN compatibility_key TEXT NOT NULL DEFAULT '';

ALTER TABLE daemon_validation_requests
    ADD COLUMN isolation_mode TEXT NOT NULL DEFAULT 'legacy';

ALTER TABLE daemon_validation_requests
    ADD COLUMN environment_fingerprint TEXT NOT NULL DEFAULT 'legacy';

ALTER TABLE daemon_validation_requests
    ADD COLUMN override_kind TEXT NOT NULL DEFAULT 'none'
    CHECK (override_kind IN ('none','no_reuse','force_rerun','emergency_skip'));

ALTER TABLE daemon_validation_requests
    ADD COLUMN override_actor TEXT NOT NULL DEFAULT '';

ALTER TABLE daemon_validation_requests
    ADD COLUMN override_reason TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_daemon_validation_compatibility
    ON daemon_validation_requests(project_id, compatibility_key, state, sequence);
