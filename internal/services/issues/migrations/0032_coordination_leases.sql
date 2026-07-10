CREATE TABLE IF NOT EXISTS issue_coordination_leases (
	issue_id TEXT NOT NULL,
	purpose TEXT NOT NULL CHECK (purpose IN ('execution', 'orchestration', 'review')),
	owner_id TEXT NOT NULL,
	owner_kind TEXT NOT NULL,
	claimed_at TEXT NOT NULL,
	expires_at TEXT,
	PRIMARY KEY (issue_id, purpose),
	FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_issue_coordination_leases_owner
	ON issue_coordination_leases (purpose, owner_id, expires_at);

INSERT OR IGNORE INTO issue_coordination_leases (
	issue_id, purpose, owner_id, owner_kind, claimed_at, expires_at
)
SELECT id, 'execution', owner_id, COALESCE(NULLIF(owner_kind, ''), 'agent'),
	COALESCE(NULLIF(owner_claimed_at, ''), updated_at), owner_expires_at
FROM issues
WHERE owner_id IS NOT NULL AND owner_id != '';
