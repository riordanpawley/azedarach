CREATE TABLE task_creation_intents (
	project_id TEXT NOT NULL CHECK (trim(project_id) <> ''),
	intent_key TEXT NOT NULL CHECK (trim(intent_key) <> ''),
	request_digest TEXT NOT NULL CHECK (trim(request_digest) <> ''),
	issue_id TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY (project_id, intent_key),
	FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX idx_task_creation_intents_issue
	ON task_creation_intents (project_id, issue_id);
