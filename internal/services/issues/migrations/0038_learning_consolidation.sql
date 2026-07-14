ALTER TABLE agent_learnings ADD COLUMN consolidated_into_id INTEGER REFERENCES agent_learnings(id) ON DELETE SET NULL;

CREATE TABLE agent_learning_suggestions (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	local_id TEXT NOT NULL UNIQUE,
	project_id TEXT NOT NULL,
	kind TEXT NOT NULL CHECK (kind IN ('duplicate', 'conflict')),
	left_learning_id INTEGER NOT NULL REFERENCES agent_learnings(id),
	right_learning_id INTEGER NOT NULL REFERENCES agent_learnings(id),
	score INTEGER NOT NULL CHECK (score BETWEEN 0 AND 100),
	reason TEXT NOT NULL,
	status TEXT NOT NULL CHECK (status IN ('pending', 'rejected', 'confirmed')),
	review_note TEXT,
	canonical_learning_id INTEGER REFERENCES agent_learnings(id),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE(project_id, kind, left_learning_id, right_learning_id)
);

CREATE INDEX idx_agent_learning_suggestions_review
	ON agent_learning_suggestions(project_id, status, score DESC, local_id);

CREATE TABLE agent_learning_consolidation_members (
	suggestion_id INTEGER NOT NULL REFERENCES agent_learning_suggestions(id) ON DELETE CASCADE,
	learning_id INTEGER NOT NULL REFERENCES agent_learnings(id),
	role TEXT NOT NULL CHECK (role IN ('canonical', 'source')),
	snapshot_json TEXT NOT NULL,
	PRIMARY KEY(suggestion_id, learning_id)
);

CREATE TABLE agent_learning_consolidation_audit (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	suggestion_id INTEGER NOT NULL REFERENCES agent_learning_suggestions(id) ON DELETE CASCADE,
	action TEXT NOT NULL CHECK (action IN ('suggested', 'rejected', 'confirmed')),
	detail_json TEXT NOT NULL,
	created_at TEXT NOT NULL
);
