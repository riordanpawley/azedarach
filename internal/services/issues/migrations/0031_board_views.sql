CREATE TABLE IF NOT EXISTS board_views (
	project_id TEXT NOT NULL,
	id TEXT NOT NULL,
	name TEXT NOT NULL,
	definition_json TEXT NOT NULL,
	built_in INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	deleted_at TEXT,
	PRIMARY KEY (project_id, id)
);

CREATE INDEX IF NOT EXISTS idx_board_views_project_active
	ON board_views (project_id, deleted_at, built_in, name);
