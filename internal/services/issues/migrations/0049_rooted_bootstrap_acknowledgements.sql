CREATE TABLE IF NOT EXISTS daemon_rooted_bootstrap_acknowledgements (
	project_id TEXT NOT NULL CHECK (trim(project_id) <> ''),
	root_issue_id TEXT NOT NULL CHECK (trim(root_issue_id) <> ''),
	session_id TEXT NOT NULL CHECK (trim(session_id) <> ''),
	prompt_hash TEXT NOT NULL CHECK (trim(prompt_hash) <> ''),
	runtime_nonce TEXT NOT NULL CHECK (trim(runtime_nonce) <> ''),
	acknowledged_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (project_id, root_issue_id),
	UNIQUE (project_id, session_id)
);
