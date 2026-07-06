CREATE TABLE IF NOT EXISTS issue_observation_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	issue_id TEXT NOT NULL,
	event_type TEXT NOT NULL,
	observed_at TEXT NOT NULL,
	source TEXT NOT NULL DEFAULT '',
	source_command TEXT NOT NULL DEFAULT '',
	operation_id TEXT NOT NULL DEFAULT '',
	session_id TEXT NOT NULL DEFAULT '',
	worktree_path TEXT NOT NULL DEFAULT '',
	payload_json TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_issue_observation_events_issue_id_id
	ON issue_observation_events(issue_id, id);

CREATE INDEX IF NOT EXISTS idx_issue_observation_events_issue_type_id
	ON issue_observation_events(issue_id, event_type, id);

CREATE INDEX IF NOT EXISTS idx_issue_observation_events_issue_observed_id
	ON issue_observation_events(issue_id, observed_at DESC, id DESC);
