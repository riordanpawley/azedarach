CREATE TABLE IF NOT EXISTS daemon_session_projections (
	project_id TEXT NOT NULL,
	session_id TEXT NOT NULL,
	issue_id TEXT NOT NULL,
	state TEXT NOT NULL,
	started_at TEXT,
	updated_at TEXT NOT NULL,
	tmux_attached_count INTEGER NOT NULL DEFAULT 0,
	observed_state TEXT,
	activity TEXT,
	activity_source TEXT,
	PRIMARY KEY (project_id, session_id)
);

CREATE TABLE IF NOT EXISTS daemon_session_observations (
	project_id TEXT NOT NULL,
	session_id TEXT NOT NULL,
	issue_id TEXT NOT NULL,
	state TEXT NOT NULL,
	observed_state TEXT,
	activity TEXT,
	activity_source TEXT,
	tmux_attached_count INTEGER NOT NULL DEFAULT 0,
	started_at TEXT,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (project_id, session_id)
);

CREATE INDEX IF NOT EXISTS idx_daemon_session_projections_project_issue_updated
	ON daemon_session_projections (project_id, issue_id, updated_at DESC, session_id DESC);

CREATE INDEX IF NOT EXISTS idx_daemon_session_observations_project_issue
	ON daemon_session_observations (project_id, issue_id);

CREATE INDEX IF NOT EXISTS idx_daemon_session_observations_project_issue_updated
	ON daemon_session_observations (project_id, issue_id, updated_at DESC, session_id DESC);
