CREATE TABLE IF NOT EXISTS decisions (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	local_id TEXT NOT NULL,
	title TEXT NOT NULL,
	rationale TEXT,
	context TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	deleted_at TEXT
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_decisions_active_local_id
	ON decisions(local_id)
	WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_decisions_updated
	ON decisions(updated_at DESC, local_id)
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

CREATE TABLE IF NOT EXISTS decision_audit_log (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	entity_type TEXT NOT NULL,
	entity_id TEXT NOT NULL,
	operation TEXT NOT NULL,
	actor_source TEXT NOT NULL,
	before_json TEXT NOT NULL,
	after_json TEXT NOT NULL,
	created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_decision_audit_entity_created_at
	ON decision_audit_log(entity_type, entity_id, created_at, id);

CREATE INDEX IF NOT EXISTS idx_decision_audit_created_at
	ON decision_audit_log(created_at, id);
