CREATE TABLE IF NOT EXISTS daemon_advisor_sessions (
    project_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    issue_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (project_id, request_id),
    UNIQUE (project_id, session_id)
);
