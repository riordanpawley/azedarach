CREATE TABLE IF NOT EXISTS orchestration_start_attempts (
	project_id TEXT NOT NULL,
	issue_id TEXT NOT NULL,
	intent_key TEXT NOT NULL,
	actor_id TEXT NOT NULL,
	dedupe_key TEXT NOT NULL,
	state TEXT NOT NULL CHECK (state IN ('claimed', 'submitted', 'compensated')),
	claim_acquired INTEGER NOT NULL CHECK (claim_acquired IN (0, 1)),
	operation_id TEXT,
	last_error TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (project_id, issue_id, intent_key),
	FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_orchestration_start_attempts_active_issue
	ON orchestration_start_attempts (project_id, issue_id)
	WHERE state = 'claimed';

CREATE INDEX IF NOT EXISTS idx_orchestration_start_attempts_recovery
	ON orchestration_start_attempts (project_id, state, updated_at, issue_id);
