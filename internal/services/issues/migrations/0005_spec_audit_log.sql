CREATE TABLE IF NOT EXISTS spec_audit_log (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	entity_type TEXT NOT NULL,
	entity_id TEXT NOT NULL,
	operation TEXT NOT NULL,
	actor_source TEXT NOT NULL,
	before_json TEXT NOT NULL,
	after_json TEXT NOT NULL,
	created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_spec_audit_entity_created_at
	ON spec_audit_log(entity_type, entity_id, created_at, id);

CREATE INDEX IF NOT EXISTS idx_spec_audit_created_at
	ON spec_audit_log(created_at, id);
