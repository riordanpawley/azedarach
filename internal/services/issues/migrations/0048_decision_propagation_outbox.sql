CREATE TABLE decision_propagation_outbox (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	decision_id TEXT NOT NULL,
	revision INTEGER NOT NULL CHECK (revision > 0),
	issue_id TEXT NOT NULL,
	event_kind TEXT NOT NULL CHECK (event_kind IN ('changed', 'withdrawn')),
	source_command TEXT NOT NULL DEFAULT '',
	payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
	created_at TEXT NOT NULL,
	materialized_event_id INTEGER,
	retired_at TEXT,
	UNIQUE (decision_id, revision, issue_id, event_kind),
	FOREIGN KEY (revision) REFERENCES decision_audit_log(id)
);

CREATE INDEX idx_decision_propagation_outbox_active
	ON decision_propagation_outbox(id)
	WHERE retired_at IS NULL;

CREATE INDEX idx_decision_propagation_outbox_issue_revision
	ON decision_propagation_outbox(issue_id, decision_id, revision);
