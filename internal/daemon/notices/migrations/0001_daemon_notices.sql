CREATE TABLE IF NOT EXISTS daemon_notices (
    notice_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    scope_type TEXT NOT NULL,
    scope_id TEXT NOT NULL DEFAULT '',
    source_json TEXT,
    source_operation_id TEXT NOT NULL DEFAULT '',
    severity TEXT NOT NULL,
    category TEXT NOT NULL,
    state TEXT NOT NULL,
    read INTEGER NOT NULL DEFAULT 0,
    title TEXT NOT NULL,
    summary TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    cause_json TEXT,
    actions_json TEXT NOT NULL DEFAULT '[]',
    dedupe_key TEXT,
    occurrence_count INTEGER NOT NULL DEFAULT 1,
    first_occurrence_at TEXT NOT NULL,
    last_occurrence_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    resolved_at TEXT,
    dismissed_at TEXT,
    expires_at TEXT,
    retention_class TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_daemon_notices_project_state_updated
    ON daemon_notices (project_id, state, updated_at DESC);

CREATE INDEX IF NOT EXISTS idx_daemon_notices_project_read_state_updated
    ON daemon_notices (project_id, read, state, updated_at DESC);

CREATE INDEX IF NOT EXISTS idx_daemon_notices_project_dedupe
    ON daemon_notices (project_id, dedupe_key);

CREATE INDEX IF NOT EXISTS idx_daemon_notices_project_operation
    ON daemon_notices (project_id, source_operation_id);

CREATE INDEX IF NOT EXISTS idx_daemon_notices_project_scope_updated
    ON daemon_notices (project_id, scope_type, scope_id, updated_at DESC);
