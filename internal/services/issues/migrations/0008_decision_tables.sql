CREATE TABLE IF NOT EXISTS decisions (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	local_id TEXT NOT NULL,
	title TEXT NOT NULL,
	context TEXT,
	decision TEXT,
	consequences TEXT,
	status TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	deleted_at TEXT
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_decisions_active_local_id
	ON decisions(local_id)
	WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_decisions_status_updated
	ON decisions(status, updated_at DESC)
	WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS decision_links (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	decision_id INTEGER NOT NULL,
	target_kind TEXT NOT NULL,
	target_id TEXT NOT NULL,
	relation TEXT NOT NULL,
	note TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	deleted_at TEXT,
	FOREIGN KEY (decision_id) REFERENCES decisions(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_decision_links_active_unique
	ON decision_links(decision_id, target_kind, target_id)
	WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_decision_links_target
	ON decision_links(target_kind, target_id, updated_at DESC)
	WHERE deleted_at IS NULL;
