ALTER TABLE issues ADD COLUMN owner_id TEXT;
ALTER TABLE issues ADD COLUMN owner_kind TEXT;
ALTER TABLE issues ADD COLUMN owner_claimed_at TEXT;
ALTER TABLE issues ADD COLUMN owner_expires_at TEXT;

CREATE INDEX IF NOT EXISTS idx_issues_owner_active
	ON issues (owner_id, owner_expires_at)
	WHERE deleted_at IS NULL AND owner_id IS NOT NULL;
