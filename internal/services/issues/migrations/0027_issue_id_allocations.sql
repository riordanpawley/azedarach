CREATE TABLE IF NOT EXISTS issue_id_allocations (
    id TEXT PRIMARY KEY,
    allocated_at TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT ''
);
