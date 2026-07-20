-- Migration 0060: separate the daemon launch-incarnation fence from the
-- hook-reported native agent thread identity used for exact resume.
--
-- Schema effects: add a nullable agent_thread_id to managed agent identities,
-- and exact pane/PID/incarnation/thread fields to rooted bootstrap records.
-- Data effects: existing identities remain NULL and therefore fail closed for
-- exact-thread restart; no historical thread identity is guessed.
-- Validation effects: startup schema validation requires the new column.
-- Ledger effects: the migration runner records this immutable artifact and its
-- pinned checksum transactionally with the ALTER TABLE.

ALTER TABLE daemon_managed_agent_incarnations
  ADD COLUMN agent_thread_id TEXT
  CHECK (agent_thread_id IS NULL OR trim(agent_thread_id) <> '');

ALTER TABLE agent_input_delivery_intents
  ADD COLUMN agent_thread_id TEXT
  CHECK (agent_thread_id IS NULL OR trim(agent_thread_id) <> '');

ALTER TABLE daemon_rooted_bootstrap_acknowledgements
  ADD COLUMN tmux_pane_id TEXT;
ALTER TABLE daemon_rooted_bootstrap_acknowledgements
  ADD COLUMN pane_pid INTEGER;
ALTER TABLE daemon_rooted_bootstrap_acknowledgements
  ADD COLUMN agent_incarnation TEXT;
ALTER TABLE daemon_rooted_bootstrap_acknowledgements
  ADD COLUMN agent_thread_id TEXT;
