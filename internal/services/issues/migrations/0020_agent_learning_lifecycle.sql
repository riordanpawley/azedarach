ALTER TABLE agent_learnings ADD COLUMN expires_at TEXT;
ALTER TABLE agent_learnings ADD COLUMN stale_at TEXT;
ALTER TABLE agent_learnings ADD COLUMN last_recalled_at TEXT;
ALTER TABLE agent_learnings ADD COLUMN recall_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agent_learnings ADD COLUMN superseded_at TEXT;
ALTER TABLE agent_learnings ADD COLUMN target_retired_at TEXT;

CREATE INDEX IF NOT EXISTS idx_agent_learnings_active_policy
	ON agent_learnings(project_id, status, expires_at, stale_at, updated_at DESC, local_id)
	WHERE deleted_at IS NULL;
