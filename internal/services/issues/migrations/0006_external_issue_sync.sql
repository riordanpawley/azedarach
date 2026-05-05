CREATE TABLE IF NOT EXISTS azedarach_external_issue_refs (
	provider TEXT NOT NULL,
	issue_id TEXT NOT NULL,
	external_id TEXT NOT NULL,
	external_identifier TEXT NOT NULL,
	external_url TEXT,
	external_updated_at TEXT,
	last_synced_at TEXT NOT NULL,
	last_sync_hash TEXT NOT NULL,
	PRIMARY KEY (provider, issue_id),
	UNIQUE (provider, external_id),
	UNIQUE (provider, external_identifier),
	FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_azedarach_external_issue_refs_external_identifier
	ON azedarach_external_issue_refs(provider, external_identifier);

CREATE TABLE IF NOT EXISTS azedarach_external_sync_state (
	provider TEXT NOT NULL,
	project_id TEXT NOT NULL,
	cursor TEXT,
	last_success_at TEXT,
	last_error TEXT,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (provider, project_id)
);

CREATE TABLE IF NOT EXISTS azedarach_external_sync_conflicts (
	id TEXT PRIMARY KEY,
	provider TEXT NOT NULL,
	project_id TEXT NOT NULL,
	issue_id TEXT NOT NULL,
	field TEXT NOT NULL,
	local_value TEXT,
	remote_value TEXT,
	local_updated_at TEXT,
	remote_updated_at TEXT,
	detected_at TEXT NOT NULL,
	resolved_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_azedarach_external_sync_conflicts_unresolved
	ON azedarach_external_sync_conflicts(provider, project_id, resolved_at);
