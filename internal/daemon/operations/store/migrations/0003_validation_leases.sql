-- Authority: project.daemon_operations.
-- Schema: add durable validation request/state tables, queue/expiry indexes,
-- one-active-aggregate enforcement, and monotonic revision triggers.
-- Data: no existing rows are rewritten or removed.
-- Validation: startup verifies every declared table, constraint, index, and
-- trigger definition and fails closed on applied-ledger schema drift.
-- Ledger: the shared migration runner records this artifact ID and pinned
-- SHA-256 exactly once in schema_migrations within the schema transaction.

CREATE TABLE daemon_validation_requests (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    request_id TEXT NOT NULL UNIQUE,
    lease_token_hash TEXT NOT NULL CHECK (length(lease_token_hash) = 64),
    project_id TEXT NOT NULL,
    issue_id TEXT NOT NULL,
    class TEXT NOT NULL CHECK (class IN ('aggregate','shared','safe')),
    profile TEXT NOT NULL,
    command TEXT NOT NULL,
    source_revision TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('queued','active','completed','cancelled','expired','failed')),
    queued_at TEXT NOT NULL,
    started_at TEXT,
    heartbeat_at TEXT,
    expires_at TEXT,
    finished_at TEXT,
    outcome TEXT NOT NULL DEFAULT '',
    evidence_json TEXT NOT NULL DEFAULT '{}'
);

CREATE UNIQUE INDEX idx_daemon_validation_one_active_aggregate
    ON daemon_validation_requests(project_id)
    WHERE state = 'active' AND class = 'aggregate';

CREATE INDEX idx_daemon_validation_project_queue
    ON daemon_validation_requests(project_id, state, sequence);

CREATE INDEX idx_daemon_validation_expiry
    ON daemon_validation_requests(state, expires_at);

CREATE TABLE daemon_validation_state (
    project_id TEXT PRIMARY KEY,
    revision INTEGER NOT NULL CHECK (revision > 0)
);

CREATE TRIGGER daemon_validation_requests_insert_revision
AFTER INSERT ON daemon_validation_requests
BEGIN
    INSERT INTO daemon_validation_state(project_id, revision)
    VALUES(NEW.project_id, 1)
    ON CONFLICT(project_id) DO UPDATE SET revision = revision + 1;
END;

CREATE TRIGGER daemon_validation_requests_update_revision
AFTER UPDATE ON daemon_validation_requests
BEGIN
    INSERT INTO daemon_validation_state(project_id, revision)
    VALUES(NEW.project_id, 1)
    ON CONFLICT(project_id) DO UPDATE SET revision = revision + 1;
END;
