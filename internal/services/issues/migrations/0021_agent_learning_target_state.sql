ALTER TABLE agent_learnings ADD COLUMN target_state TEXT;
ALTER TABLE agent_learnings ADD COLUMN target_hash TEXT;
ALTER TABLE agent_learnings ADD COLUMN target_metadata_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE agent_learnings ADD COLUMN target_drifted_at TEXT;

UPDATE agent_learnings
SET target_state = CASE
	WHEN target_retired_at IS NOT NULL THEN 'retired'
	WHEN status = 'promoted' OR promotion_target IS NOT NULL THEN 'active'
	ELSE NULL
END
WHERE deleted_at IS NULL
	AND (status = 'promoted' OR promotion_target IS NOT NULL OR target_retired_at IS NOT NULL);

CREATE INDEX IF NOT EXISTS idx_agent_learnings_target_state
	ON agent_learnings(project_id, target_state, updated_at DESC, local_id)
	WHERE deleted_at IS NULL AND status = 'promoted';
