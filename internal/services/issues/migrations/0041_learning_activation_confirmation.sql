CREATE TABLE IF NOT EXISTS learning_activation_proposals (
    activation_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    surface TEXT NOT NULL,
    context_fingerprint TEXT NOT NULL,
    learning_ids_json TEXT NOT NULL,
    explanation TEXT NOT NULL,
    purpose TEXT NOT NULL,
    session_id TEXT NOT NULL,
    proposed_at TEXT NOT NULL
);
ALTER TABLE learning_activations ADD COLUMN resolved_outcome TEXT NOT NULL DEFAULT '' CHECK(resolved_outcome IN ('','helpful','followed','contradicted','unknown'));
ALTER TABLE learning_activations ADD COLUMN resolved_source TEXT NOT NULL DEFAULT '' CHECK(resolved_source IN ('','explicit','human','agent','inferred'));
ALTER TABLE learning_activation_outcomes RENAME TO learning_activation_outcomes_legacy;
CREATE TABLE learning_activation_outcomes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    activation_id TEXT NOT NULL REFERENCES learning_activations(activation_id) ON DELETE CASCADE,
    idempotency_key TEXT NOT NULL,
    outcome TEXT NOT NULL CHECK(outcome IN ('helpful','followed','contradicted','unknown')),
    source TEXT NOT NULL CHECK(source IN ('explicit','human','agent','inferred')),
    explanation TEXT NOT NULL DEFAULT '',
    recorded_at TEXT NOT NULL,
    UNIQUE(activation_id, idempotency_key)
);
INSERT INTO learning_activation_outcomes SELECT * FROM learning_activation_outcomes_legacy;
DROP TABLE learning_activation_outcomes_legacy;
UPDATE learning_activations
SET resolved_outcome = (SELECT o.outcome FROM learning_activation_outcomes o WHERE o.activation_id=learning_activations.activation_id ORDER BY CASE o.source WHEN 'human' THEN 3 WHEN 'explicit' THEN 3 WHEN 'agent' THEN 2 ELSE 1 END DESC, o.id ASC LIMIT 1),
    resolved_source = (SELECT o.source FROM learning_activation_outcomes o WHERE o.activation_id=learning_activations.activation_id ORDER BY CASE o.source WHEN 'human' THEN 3 WHEN 'explicit' THEN 3 WHEN 'agent' THEN 2 ELSE 1 END DESC, o.id ASC LIMIT 1)
WHERE EXISTS (SELECT 1 FROM learning_activation_outcomes o WHERE o.activation_id=learning_activations.activation_id);
