-- Migration 0052 manifest
--
-- Schema effects:
--   Expand agent input delivery intents with the ambiguous submission state
--   and create durable session-scoped delivery leases with fenced incarnation
--   values and an expiry index.
-- Data effects:
--   Rebuild agent_input_delivery_intents transactionally and copy every
--   existing row without changing values. Existing 0051 states all satisfy
--   the expanded constraint. The new session lease table starts empty.
-- Validation effects:
--   The Go-assisted runner executes this artifact transactionally and validates
--   the final table SQL, required columns, constraints, and indexes before the
--   transaction may write its ledger row or commit. Every later startup repeats
--   the same final-schema validation when this migration is recorded applied.
-- Ledger effects:
--   After schema and data validation, the runner records exactly one
--   schema_migrations row for 0052_agent_input_delivery_fencing with this
--   artifact's pinned SHA-256 checksum. Schema, copied data, validation, and
--   ledger mutation roll back together on any failure.

DROP INDEX idx_agent_input_delivery_pending;
DROP INDEX idx_agent_input_delivery_incarnation;

ALTER TABLE agent_input_delivery_intents RENAME TO agent_input_delivery_intents_0051;

CREATE TABLE agent_input_delivery_intents (
  project_id TEXT NOT NULL CHECK (trim(project_id) <> ''),
  intent_key TEXT NOT NULL CHECK (trim(intent_key) <> ''),
  session_id TEXT NOT NULL CHECK (trim(session_id) <> ''),
  logical_pane_id TEXT NOT NULL CHECK (trim(logical_pane_id) <> ''),
  tmux_pane_id TEXT NOT NULL CHECK (trim(tmux_pane_id) <> ''),
  pane_pid INTEGER NOT NULL CHECK (pane_pid > 0),
  agent_incarnation TEXT NOT NULL CHECK (trim(agent_incarnation) <> ''),
  tool TEXT NOT NULL CHECK (trim(tool) <> ''),
  message_kind TEXT NOT NULL CHECK (trim(message_kind) <> ''),
  payload TEXT NOT NULL CHECK (length(payload) > 0),
  state TEXT NOT NULL CHECK (state IN ('queued','leased','ambiguous','delivered','expired','stale')),
  expires_at TEXT,
  lease_owner TEXT,
  lease_token TEXT,
  lease_expires_at TEXT,
  attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  acknowledgement_token TEXT,
  acknowledged_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (project_id, intent_key),
  CHECK ((state = 'delivered') = (acknowledgement_token IS NOT NULL AND acknowledged_at IS NOT NULL)),
  CHECK ((state IN ('leased','ambiguous')) = (lease_owner IS NOT NULL AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL))
);

INSERT INTO agent_input_delivery_intents(
  project_id, intent_key, session_id, logical_pane_id, tmux_pane_id, pane_pid,
  agent_incarnation, tool, message_kind, payload, state, expires_at,
  lease_owner, lease_token, lease_expires_at, attempt_count,
  acknowledgement_token, acknowledged_at, created_at, updated_at
)
SELECT
  project_id, intent_key, session_id, logical_pane_id, tmux_pane_id, pane_pid,
  agent_incarnation, tool, message_kind, payload, state, expires_at,
  lease_owner, lease_token, lease_expires_at, attempt_count,
  acknowledgement_token, acknowledged_at, created_at, updated_at
FROM agent_input_delivery_intents_0051;

DROP TABLE agent_input_delivery_intents_0051;

CREATE INDEX idx_agent_input_delivery_pending
  ON agent_input_delivery_intents(project_id, state, expires_at, created_at)
  WHERE state IN ('queued','leased');

CREATE INDEX idx_agent_input_delivery_incarnation
  ON agent_input_delivery_intents(project_id, session_id, logical_pane_id, agent_incarnation, state);

CREATE TABLE agent_input_delivery_session_leases (
  project_id TEXT NOT NULL CHECK (trim(project_id) <> ''),
  session_id TEXT NOT NULL CHECK (trim(session_id) <> ''),
  agent_incarnation TEXT NOT NULL CHECK (trim(agent_incarnation) <> ''),
  lease_owner TEXT NOT NULL CHECK (trim(lease_owner) <> ''),
  lease_token TEXT NOT NULL CHECK (trim(lease_token) <> ''),
  lease_expires_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (project_id, session_id)
);

CREATE INDEX idx_agent_input_session_lease_expiry
  ON agent_input_delivery_session_leases(lease_expires_at);
