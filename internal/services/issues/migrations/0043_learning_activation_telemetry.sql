ALTER TABLE learning_activation_proposals ADD COLUMN status TEXT NOT NULL DEFAULT 'proposed' CHECK(status IN ('proposed','confirmed','abandoned'));
ALTER TABLE learning_activation_proposals ADD COLUMN confirmed_at TEXT;
CREATE INDEX IF NOT EXISTS idx_learning_activation_proposals_project_time ON learning_activation_proposals(project_id, proposed_at, status);
CREATE TABLE IF NOT EXISTS learning_activation_exclusions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL,
    surface TEXT NOT NULL,
    purpose TEXT NOT NULL,
    session_id TEXT NOT NULL,
    reason TEXT NOT NULL CHECK(reason IN ('suppressed','budget')),
    learning_count INTEGER NOT NULL CHECK(learning_count >= 0),
    recorded_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_learning_activation_exclusions_project_time ON learning_activation_exclusions(project_id, recorded_at, reason);
