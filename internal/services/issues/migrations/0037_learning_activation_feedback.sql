CREATE TABLE IF NOT EXISTS learning_activations (
    activation_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    surface TEXT NOT NULL CHECK(length(trim(surface)) > 0),
    context_fingerprint TEXT NOT NULL,
    learning_ids_json TEXT NOT NULL,
    token_cost INTEGER NOT NULL CHECK(token_cost >= 0 AND token_cost <= 32768),
    explanation TEXT NOT NULL,
    delivered_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_learning_activations_project_delivered ON learning_activations(project_id, delivered_at DESC);
CREATE TABLE IF NOT EXISTS learning_activation_outcomes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    activation_id TEXT NOT NULL REFERENCES learning_activations(activation_id) ON DELETE CASCADE,
    idempotency_key TEXT NOT NULL,
    outcome TEXT NOT NULL CHECK(outcome IN ('helpful','followed','contradicted','unknown')),
    source TEXT NOT NULL CHECK(source IN ('explicit','inferred')),
    explanation TEXT NOT NULL DEFAULT '',
    recorded_at TEXT NOT NULL,
    UNIQUE(activation_id, idempotency_key)
);
