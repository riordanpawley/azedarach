CREATE TABLE IF NOT EXISTS agent_learnings (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	local_id TEXT NOT NULL,
	project_id TEXT NOT NULL DEFAULT 'default',
	issue_id TEXT,
	requirement_id TEXT,
	session_id TEXT,
	summary TEXT NOT NULL,
	evidence TEXT NOT NULL,
	status TEXT NOT NULL,
	review_note TEXT,
	reviewed_at TEXT,
	tags_json TEXT NOT NULL DEFAULT '[]',
	files_json TEXT NOT NULL DEFAULT '[]',
	promotion_target TEXT,
	promotion_target_id TEXT,
	promotion_note TEXT,
	promoted_at TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	deleted_at TEXT,
	FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE SET NULL,
	FOREIGN KEY (requirement_id) REFERENCES spec_requirements(id) ON DELETE SET NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_learnings_active_local_id
	ON agent_learnings(local_id)
	WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_agent_learnings_project_status_updated
	ON agent_learnings(project_id, status, updated_at DESC, local_id)
	WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_agent_learnings_issue_updated
	ON agent_learnings(issue_id, updated_at DESC, local_id)
	WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_agent_learnings_requirement_updated
	ON agent_learnings(requirement_id, updated_at DESC, local_id)
	WHERE deleted_at IS NULL;

DROP TRIGGER IF EXISTS agent_learnings_ai_search_fts;
DROP TRIGGER IF EXISTS agent_learnings_au_search_fts;
DROP TRIGGER IF EXISTS agent_learnings_ad_search_fts;
DROP TABLE IF EXISTS agent_learning_search_fts;

CREATE VIRTUAL TABLE agent_learning_search_fts USING fts5(
	local_id,
	project_id,
	issue_id,
	requirement_id,
	session_id,
	summary,
	status,
	tags,
	files,
	content = '',
	detail = none,
	tokenize = 'unicode61'
);

CREATE TRIGGER agent_learnings_ai_search_fts
AFTER INSERT ON agent_learnings
WHEN NEW.deleted_at IS NULL
BEGIN
	INSERT INTO agent_learning_search_fts (
		rowid, local_id, project_id, issue_id, requirement_id, session_id,
		summary, status, tags, files
	)
	VALUES (
		NEW.rowid, COALESCE(NEW.local_id, ''), COALESCE(NEW.project_id, ''),
		COALESCE(NEW.issue_id, ''), COALESCE(NEW.requirement_id, ''),
		COALESCE(NEW.session_id, ''), COALESCE(NEW.summary, ''),
		COALESCE(NEW.status, ''), COALESCE(NEW.tags_json, ''),
		COALESCE(NEW.files_json, '')
	);
END;

CREATE TRIGGER agent_learnings_au_search_fts
AFTER UPDATE ON agent_learnings
BEGIN
	INSERT INTO agent_learning_search_fts (
		agent_learning_search_fts, rowid, local_id, project_id, issue_id,
		requirement_id, session_id, summary, status, tags, files
	)
	SELECT
		'delete', OLD.rowid, COALESCE(OLD.local_id, ''), COALESCE(OLD.project_id, ''),
		COALESCE(OLD.issue_id, ''), COALESCE(OLD.requirement_id, ''),
		COALESCE(OLD.session_id, ''), COALESCE(OLD.summary, ''),
		COALESCE(OLD.status, ''), COALESCE(OLD.tags_json, ''),
		COALESCE(OLD.files_json, '')
	WHERE OLD.deleted_at IS NULL;

	INSERT INTO agent_learning_search_fts (
		rowid, local_id, project_id, issue_id, requirement_id, session_id,
		summary, status, tags, files
	)
	SELECT
		NEW.rowid, COALESCE(NEW.local_id, ''), COALESCE(NEW.project_id, ''),
		COALESCE(NEW.issue_id, ''), COALESCE(NEW.requirement_id, ''),
		COALESCE(NEW.session_id, ''), COALESCE(NEW.summary, ''),
		COALESCE(NEW.status, ''), COALESCE(NEW.tags_json, ''),
		COALESCE(NEW.files_json, '')
	WHERE NEW.deleted_at IS NULL;
END;

CREATE TRIGGER agent_learnings_ad_search_fts
AFTER DELETE ON agent_learnings
WHEN OLD.deleted_at IS NULL
BEGIN
	INSERT INTO agent_learning_search_fts (
		agent_learning_search_fts, rowid, local_id, project_id, issue_id,
		requirement_id, session_id, summary, status, tags, files
	)
	VALUES (
		'delete', OLD.rowid, COALESCE(OLD.local_id, ''), COALESCE(OLD.project_id, ''),
		COALESCE(OLD.issue_id, ''), COALESCE(OLD.requirement_id, ''),
		COALESCE(OLD.session_id, ''), COALESCE(OLD.summary, ''),
		COALESCE(OLD.status, ''), COALESCE(OLD.tags_json, ''),
		COALESCE(OLD.files_json, '')
	);
END;

INSERT INTO agent_learning_search_fts (
	rowid, local_id, project_id, issue_id, requirement_id, session_id,
	summary, status, tags, files
)
SELECT
	rowid, COALESCE(local_id, ''), COALESCE(project_id, ''),
	COALESCE(issue_id, ''), COALESCE(requirement_id, ''),
	COALESCE(session_id, ''), COALESCE(summary, ''),
	COALESCE(status, ''), COALESCE(tags_json, ''),
	COALESCE(files_json, '')
FROM agent_learnings
WHERE deleted_at IS NULL;
