CREATE TABLE IF NOT EXISTS spec_requirements (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	local_id TEXT NOT NULL,
	external_code TEXT,
	title TEXT NOT NULL,
	description TEXT,
	issue_id TEXT,
	status TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	deleted_at TEXT,
	FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS spec_links (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	issue_id TEXT NOT NULL,
	requirement_id INTEGER NOT NULL,
	role TEXT NOT NULL,
	note TEXT,
	implementations_json TEXT,
	fulfillment_status TEXT,
	fulfilled_at TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	deleted_at TEXT,
	FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE,
	FOREIGN KEY (requirement_id) REFERENCES spec_requirements(id) ON DELETE CASCADE
);
