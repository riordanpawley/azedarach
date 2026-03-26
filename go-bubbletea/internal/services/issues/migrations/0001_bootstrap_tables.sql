CREATE TABLE IF NOT EXISTS issues (
	id TEXT PRIMARY KEY,
	title TEXT NOT NULL,
	description TEXT,
	status TEXT NOT NULL,
	priority INTEGER NOT NULL,
	issue_type TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	closed_at TEXT,
	assignee TEXT,
	labels_json TEXT,
	implementations_json TEXT,
	design TEXT,
	notes TEXT,
	acceptance TEXT,
	estimate INTEGER,
	deleted_at TEXT
);

CREATE TABLE IF NOT EXISTS meta (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS issue_dependencies (
	issue_id TEXT NOT NULL,
	depends_on_id TEXT NOT NULL,
	dependency_type TEXT NOT NULL,
	tombstoned_at TEXT,
	PRIMARY KEY (issue_id, depends_on_id, dependency_type)
);
