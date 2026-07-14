CREATE TABLE IF NOT EXISTS agent_learning_consolidation_scan_state (
	project_id TEXT PRIMARY KEY,
	cursor_local_id TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
