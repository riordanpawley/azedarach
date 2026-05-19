-- Refresh the decisions table to drop status/consequences and rename
-- the body field to "rationale". Pre-prod schema rework; no live data
-- to preserve, but the SELECT-from-old form keeps the migration idempotent
-- for repo clones that already ran 0008.

CREATE TABLE IF NOT EXISTS decisions_new (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	local_id TEXT NOT NULL,
	title TEXT NOT NULL,
	rationale TEXT,
	context TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	deleted_at TEXT
);

INSERT INTO decisions_new (id, local_id, title, rationale, context, created_at, updated_at, deleted_at)
SELECT id, local_id, title, decision, context, created_at, updated_at, deleted_at
FROM decisions;

DROP TABLE decisions;
ALTER TABLE decisions_new RENAME TO decisions;

CREATE UNIQUE INDEX IF NOT EXISTS idx_decisions_active_local_id
	ON decisions(local_id)
	WHERE deleted_at IS NULL;
