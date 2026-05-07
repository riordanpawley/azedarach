DROP TABLE IF EXISTS issue_external_refs;

CREATE TABLE IF NOT EXISTS issue_external_refs (
	issue_id TEXT NOT NULL,
	provider TEXT NOT NULL,
	provider_scope TEXT NOT NULL DEFAULT '',
	remote_key TEXT NOT NULL,
	display_key TEXT,
	url TEXT,
	metadata_json TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	deleted_at TEXT,
	PRIMARY KEY (issue_id, provider, provider_scope, remote_key),
	FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_external_refs_active_remote
	ON issue_external_refs(provider, provider_scope, remote_key)
	WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_issue_external_refs_issue_active
	ON issue_external_refs(issue_id, provider, provider_scope, updated_at DESC)
	WHERE deleted_at IS NULL;
