ALTER TABLE agent_learnings ADD COLUMN evidence_private INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_agent_learnings_active_privacy
	ON agent_learnings(project_id, status, evidence_private, updated_at DESC, local_id)
	WHERE deleted_at IS NULL;
