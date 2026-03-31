CREATE TABLE IF NOT EXISTS daemon_operations (
    operation_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    issue_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    dedupe_key TEXT,
    resource_keys_json TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('queued', 'running', 'done', 'failed', 'cancelled')),
    submitted_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    result_json TEXT,
    error_json TEXT,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_daemon_operations_project_state_updated
    ON daemon_operations (project_id, state, updated_at DESC);

CREATE INDEX IF NOT EXISTS idx_daemon_operations_issue_updated
    ON daemon_operations (issue_id, updated_at DESC);

CREATE INDEX IF NOT EXISTS idx_daemon_operations_dedupe
    ON daemon_operations (project_id, dedupe_key, updated_at DESC)
    WHERE dedupe_key IS NOT NULL;
