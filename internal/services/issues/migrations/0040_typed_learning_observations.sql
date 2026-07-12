CREATE TABLE IF NOT EXISTS learning_observations (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	local_id TEXT NOT NULL UNIQUE,
	learning_id INTEGER NOT NULL UNIQUE,
	observed_behavior TEXT NOT NULL,
	preferred_behavior TEXT NOT NULL,
	outcome TEXT NOT NULL DEFAULT '',
	impact TEXT NOT NULL DEFAULT '',
	context_json TEXT NOT NULL DEFAULT '{}',
	provenance_source TEXT NOT NULL,
	provenance_actor TEXT NOT NULL DEFAULT '',
	provenance_ref TEXT NOT NULL DEFAULT '',
	sensitivity TEXT NOT NULL CHECK (sensitivity IN ('public','private')),
	safe_fingerprint TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	FOREIGN KEY (learning_id) REFERENCES agent_learnings(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_learning_observations_safe_duplicates
	ON learning_observations(safe_fingerprint, created_at DESC)
	WHERE sensitivity = 'public' AND safe_fingerprint != '';
