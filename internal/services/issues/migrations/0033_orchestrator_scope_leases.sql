CREATE TABLE IF NOT EXISTS daemon_orchestrator_scope_leases (
	project_id TEXT NOT NULL,
	scope_kind TEXT NOT NULL CHECK (scope_kind IN ('project', 'rooted')),
	root_issue_id TEXT NOT NULL DEFAULT '',
	session_id TEXT NOT NULL,
	lifecycle TEXT NOT NULL CHECK (lifecycle IN ('working', 'quiescent', 'complete-grace', 'paused')),
	acquired_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (project_id, scope_kind, root_issue_id),
	UNIQUE (project_id, session_id),
	CHECK (
		(scope_kind = 'project' AND root_issue_id = '') OR
		(scope_kind = 'rooted' AND root_issue_id <> '')
	)
);

CREATE INDEX IF NOT EXISTS idx_daemon_orchestrator_scope_leases_project_updated
	ON daemon_orchestrator_scope_leases (project_id, updated_at DESC, scope_kind, root_issue_id);
