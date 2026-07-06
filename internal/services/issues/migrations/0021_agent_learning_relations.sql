CREATE TABLE IF NOT EXISTS agent_learning_relations (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	local_id TEXT NOT NULL,
	relation_type TEXT NOT NULL,
	source_learning_id INTEGER NOT NULL,
	target_learning_id INTEGER NOT NULL,
	note TEXT NOT NULL,
	scope_issue_id TEXT,
	scope_requirement_id TEXT,
	scope_session_id TEXT,
	scope_tags_json TEXT NOT NULL DEFAULT '[]',
	scope_files_json TEXT NOT NULL DEFAULT '[]',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	deleted_at TEXT,
	FOREIGN KEY (source_learning_id) REFERENCES agent_learnings(id) ON DELETE CASCADE,
	FOREIGN KEY (target_learning_id) REFERENCES agent_learnings(id) ON DELETE CASCADE,
	FOREIGN KEY (scope_issue_id) REFERENCES issues(id) ON DELETE SET NULL,
	FOREIGN KEY (scope_requirement_id) REFERENCES spec_requirements(id) ON DELETE SET NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_learning_relations_active_local_id
	ON agent_learning_relations(local_id)
	WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_learning_relations_active_edge
	ON agent_learning_relations(relation_type, source_learning_id, target_learning_id)
	WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_agent_learning_relations_source
	ON agent_learning_relations(source_learning_id, created_at DESC, local_id)
	WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_agent_learning_relations_target
	ON agent_learning_relations(target_learning_id, created_at DESC, local_id)
	WHERE deleted_at IS NULL;
