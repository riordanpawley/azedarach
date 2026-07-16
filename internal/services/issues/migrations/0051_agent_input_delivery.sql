-- Migration 0051 manifest
--
-- Schema effects:
--   Create agent_input_delivery_intents and its pending/incarnation indexes.
-- Data effects:
--   None. Existing issue, session, decision, and runtime rows are not read,
--   rewritten, backfilled, or deleted; the new intent table starts empty.
-- Validation effects:
--   The Go-assisted runner executes this artifact transactionally and validates
--   the table SQL, required columns, constraints, and both indexes before the
--   transaction may write its ledger row or commit. Every later startup repeats
--   the same schema validation when the ledger records this migration applied.
-- Ledger effects:
--   After schema validation, the runner records exactly one
--   schema_migrations row for 0051_agent_input_delivery with this artifact's
--   pinned SHA-256 checksum. Schema, validation, and ledger mutation roll back
--   together on any failure.

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
  state TEXT NOT NULL CHECK (state IN ('queued','leased','delivered','expired','stale')),
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
  CHECK ((state = 'leased') = (lease_owner IS NOT NULL AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL))
);

CREATE INDEX idx_agent_input_delivery_pending
  ON agent_input_delivery_intents(project_id, state, expires_at, created_at)
  WHERE state IN ('queued','leased');

CREATE INDEX idx_agent_input_delivery_incarnation
  ON agent_input_delivery_intents(project_id, session_id, logical_pane_id, agent_incarnation, state);
