CREATE TABLE projection_streams (
    project_id TEXT PRIMARY KEY,
    head_cursor INTEGER NOT NULL DEFAULT 0 CHECK (head_cursor >= 0),
    updated_at TEXT NOT NULL
);

CREATE TABLE projection_deltas (
    project_id TEXT NOT NULL,
    cursor INTEGER NOT NULL CHECK (cursor > 0),
    kind TEXT NOT NULL CHECK (trim(kind) != ''),
    key TEXT NOT NULL CHECK (trim(key) != ''),
    operation TEXT NOT NULL CHECK (operation IN ('upsert', 'delete')),
    idempotency_key TEXT NOT NULL CHECK (trim(idempotency_key) != ''),
    payload_json TEXT NOT NULL,
    committed_at TEXT NOT NULL,
    PRIMARY KEY (project_id, cursor),
    UNIQUE (project_id, idempotency_key),
    FOREIGN KEY (project_id) REFERENCES projection_streams(project_id) ON DELETE CASCADE
);

CREATE INDEX idx_projection_deltas_key_history
    ON projection_deltas(project_id, kind, key, cursor DESC);

CREATE TABLE projection_consumer_cursors (
    project_id TEXT NOT NULL,
    consumer TEXT NOT NULL CHECK (trim(consumer) != ''),
    cursor INTEGER NOT NULL DEFAULT 0 CHECK (cursor >= 0),
    updated_at TEXT NOT NULL,
    PRIMARY KEY (project_id, consumer),
    FOREIGN KEY (project_id) REFERENCES projection_streams(project_id) ON DELETE CASCADE
);
