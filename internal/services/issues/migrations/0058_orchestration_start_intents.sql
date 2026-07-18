CREATE TABLE IF NOT EXISTS orchestration_start_intents (
	project_id TEXT NOT NULL,
	issue_id TEXT NOT NULL,
	intent_key TEXT NOT NULL,
	actor_id TEXT NOT NULL,
	dedupe_key TEXT NOT NULL,
	request_digest TEXT NOT NULL,
	state TEXT NOT NULL CHECK (state IN ('queued', 'completed')),
	phase TEXT NOT NULL,
	last_error TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (project_id, issue_id, intent_key),
	FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_orchestration_start_intents_dedupe
	ON orchestration_start_intents (project_id, dedupe_key);

CREATE INDEX IF NOT EXISTS idx_orchestration_start_intents_recovery
	ON orchestration_start_intents (project_id, state, updated_at, issue_id);
