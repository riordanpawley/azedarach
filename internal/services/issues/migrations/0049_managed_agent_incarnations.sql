CREATE TABLE IF NOT EXISTS daemon_managed_agent_incarnations (
  project_id TEXT NOT NULL CHECK (trim(project_id) <> ''),
  session_id TEXT NOT NULL CHECK (trim(session_id) <> ''),
  logical_pane_id TEXT NOT NULL CHECK (trim(logical_pane_id) <> ''),
  tmux_pane_id TEXT NOT NULL CHECK (trim(tmux_pane_id) <> ''),
  pane_pid INTEGER NOT NULL CHECK (pane_pid > 0),
  agent_incarnation TEXT NOT NULL CHECK (trim(agent_incarnation) <> ''),
  observed_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (project_id, session_id, logical_pane_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_daemon_managed_agent_physical_incarnation
  ON daemon_managed_agent_incarnations(project_id, tmux_pane_id, pane_pid, agent_incarnation);
