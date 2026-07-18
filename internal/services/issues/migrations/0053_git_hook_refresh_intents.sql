CREATE TABLE IF NOT EXISTS daemon_git_hook_refresh_intents (
    project_id TEXT NOT NULL CHECK (trim(project_id) <> ''),
    worktree TEXT NOT NULL CHECK (trim(worktree) <> ''),
    requested_generation INTEGER NOT NULL CHECK (requested_generation > 0),
    completed_generation INTEGER NOT NULL DEFAULT 0 CHECK (completed_generation >= 0),
    requested_at TEXT NOT NULL,
    completed_at TEXT,
    PRIMARY KEY (project_id, worktree),
    CHECK (completed_generation <= requested_generation)
);

CREATE INDEX IF NOT EXISTS idx_daemon_git_hook_refresh_intents_pending
    ON daemon_git_hook_refresh_intents (project_id, requested_generation, completed_generation);
