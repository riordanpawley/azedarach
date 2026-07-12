ALTER TABLE learning_activations ADD COLUMN purpose TEXT NOT NULL DEFAULT '';
ALTER TABLE learning_activations ADD COLUMN session_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_learning_activations_session ON learning_activations(project_id, session_id, delivered_at DESC);
CREATE TABLE IF NOT EXISTS learning_activation_deliveries (
    activation_id TEXT NOT NULL REFERENCES learning_activations(activation_id) ON DELETE CASCADE,
    project_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    learning_id TEXT NOT NULL,
    PRIMARY KEY (project_id, session_id, learning_id)
);
CREATE INDEX IF NOT EXISTS idx_learning_activation_deliveries_activation ON learning_activation_deliveries(activation_id);
