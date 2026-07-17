-- One physical top-level tmux session has exactly one desired role/scope.
-- Historical rooted startup could leave the matching worker/issue intent next
-- to the rooted-orchestrator intent. The rooted lease and typed orchestrator
-- row are the more specific authority, so retire only that unambiguous legacy
-- pair and fail closed for every other duplicate shape.
DELETE FROM daemon_session_projections AS worker
WHERE worker.role = 'worker'
  AND worker.scope_kind = 'issue'
  AND instr(worker.session_id, '.pane-') = 0
  AND worker.scope_id = worker.issue_id
  AND EXISTS (
    SELECT 1
    FROM daemon_session_projections AS rooted
    WHERE rooted.project_id = worker.project_id
      AND rooted.session_id = worker.session_id
      AND rooted.role = 'orchestrator'
      AND rooted.scope_kind = 'orchestration'
      AND rooted.scope_id <> 'project'
      AND rooted.scope_id = worker.issue_id
  );

CREATE TABLE rooted_session_role_exclusivity_validation (
  ok INTEGER NOT NULL CHECK (ok = 1)
);
INSERT INTO rooted_session_role_exclusivity_validation(ok)
SELECT 0
WHERE EXISTS (
  SELECT 1
  FROM daemon_session_projections
  WHERE instr(session_id, '.pane-') = 0
  GROUP BY project_id, session_id
  HAVING COUNT(*) > 1
);
DROP TABLE rooted_session_role_exclusivity_validation;

CREATE UNIQUE INDEX IF NOT EXISTS idx_daemon_session_projections_physical_session_unique
  ON daemon_session_projections(project_id, session_id)
  WHERE instr(session_id, '.pane-') = 0;
