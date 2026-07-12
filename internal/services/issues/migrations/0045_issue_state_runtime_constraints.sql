-- Persistent guards for local state-product invariants. Cross-module facts
-- (notably tmux presence) remain daemon hybrid invariants.

-- Normalize valid legacy representations before installing fail-closed guards.
UPDATE issues SET closed_at=COALESCE(closed_at, updated_at)
  WHERE lifecycle_state='closed';
UPDATE issues SET closed_at=NULL
  WHERE lifecycle_state!='closed';
UPDATE issues SET owner_kind=COALESCE(NULLIF(owner_kind,''),'agent'),
  owner_claimed_at=COALESCE(owner_claimed_at,updated_at)
  WHERE owner_id IS NOT NULL;
UPDATE issues SET owner_kind=NULL, owner_claimed_at=NULL, owner_expires_at=NULL
  WHERE owner_id IS NULL;

CREATE TABLE issue_state_runtime_constraint_validation (
  ok INTEGER NOT NULL CHECK (ok = 1)
);
INSERT INTO issue_state_runtime_constraint_validation(ok)
SELECT 0 WHERE EXISTS (
  SELECT 1 FROM issues WHERE
    issue_type NOT IN ('task','bug','feature','epic','chore','investigation') OR
    lifecycle_state NOT IN ('backlog','open','active','closed') OR
    review_state NOT IN ('none','requested') OR
    closed_outcome NOT IN ('none','completed','cancelled') OR
    (review_state='requested' AND lifecycle_state!='active') OR
    (lifecycle_state='closed' AND (closed_outcome='none' OR closed_at IS NULL)) OR
    (lifecycle_state!='closed' AND (closed_outcome!='none' OR closed_at IS NOT NULL)) OR
    ((owner_id IS NULL) != (owner_kind IS NULL)) OR
    ((owner_id IS NULL) != (owner_claimed_at IS NULL))
);
INSERT INTO issue_state_runtime_constraint_validation(ok)
SELECT 0 WHERE EXISTS (
  SELECT 1 FROM daemon_session_projections WHERE
    trim(project_id)='' OR trim(session_id)='' OR trim(issue_id)='' OR
    state NOT IN ('starting','running','attached','stopping','paused','stopped','idle','busy','waiting','done','error','unknown') OR
    (observed_state IS NOT NULL AND observed_state NOT IN ('','starting','running','attached','stopping','paused','stopped','idle','busy','waiting','done','error','unknown')) OR
    tmux_attached_count < 0 OR
    ((state='stopped' OR observed_state='stopped') AND tmux_attached_count != 0)
);
INSERT INTO issue_state_runtime_constraint_validation(ok)
SELECT 0 WHERE EXISTS (
  SELECT 1 FROM daemon_worktree_projections WHERE trim(project_id)='' OR trim(issue_id)='' OR trim(path)=''
);
DROP TABLE issue_state_runtime_constraint_validation;

CREATE TRIGGER issue_state_product_guard_insert
BEFORE INSERT ON issues
BEGIN
  SELECT CASE WHEN NEW.issue_type NOT IN ('task','bug','feature','epic','chore','investigation')
    THEN RAISE(ABORT, 'invalid issue_type') END;
  SELECT CASE WHEN NEW.lifecycle_state NOT IN ('backlog','open','active','closed')
    THEN RAISE(ABORT, 'invalid lifecycle_state') END;
  SELECT CASE WHEN NEW.review_state NOT IN ('none','requested')
    THEN RAISE(ABORT, 'invalid review_state') END;
  SELECT CASE WHEN NEW.closed_outcome NOT IN ('none','completed','cancelled')
    THEN RAISE(ABORT, 'invalid closed_outcome') END;
  SELECT CASE WHEN NEW.review_state='requested' AND NEW.lifecycle_state!='active'
    THEN RAISE(ABORT, 'review requires active lifecycle') END;
  SELECT CASE WHEN NEW.lifecycle_state='closed' AND (NEW.closed_outcome='none' OR NEW.closed_at IS NULL)
    THEN RAISE(ABORT, 'terminal issue requires outcome and closed_at') END;
  SELECT CASE WHEN NEW.lifecycle_state!='closed' AND (NEW.closed_outcome!='none' OR NEW.closed_at IS NOT NULL)
    THEN RAISE(ABORT, 'non-terminal issue cannot retain outcome or closed_at') END;
  SELECT CASE WHEN (NEW.owner_id IS NULL) != (NEW.owner_kind IS NULL)
    OR (NEW.owner_id IS NULL) != (NEW.owner_claimed_at IS NULL)
    THEN RAISE(ABORT, 'issue owner fields must form a complete tuple') END;
END;

CREATE TRIGGER issue_state_product_guard_update
BEFORE UPDATE OF issue_type,lifecycle_state,review_state,closed_outcome,closed_at,owner_id,owner_kind,owner_claimed_at ON issues
BEGIN
  SELECT CASE WHEN NEW.issue_type NOT IN ('task','bug','feature','epic','chore','investigation')
    THEN RAISE(ABORT, 'invalid issue_type') END;
  SELECT CASE WHEN NEW.lifecycle_state NOT IN ('backlog','open','active','closed')
    THEN RAISE(ABORT, 'invalid lifecycle_state') END;
  SELECT CASE WHEN NEW.review_state NOT IN ('none','requested')
    THEN RAISE(ABORT, 'invalid review_state') END;
  SELECT CASE WHEN NEW.closed_outcome NOT IN ('none','completed','cancelled')
    THEN RAISE(ABORT, 'invalid closed_outcome') END;
  SELECT CASE WHEN NEW.review_state='requested' AND NEW.lifecycle_state!='active'
    THEN RAISE(ABORT, 'review requires active lifecycle') END;
  SELECT CASE WHEN NEW.lifecycle_state='closed' AND (NEW.closed_outcome='none' OR NEW.closed_at IS NULL)
    THEN RAISE(ABORT, 'terminal issue requires outcome and closed_at') END;
  SELECT CASE WHEN NEW.lifecycle_state!='closed' AND (NEW.closed_outcome!='none' OR NEW.closed_at IS NOT NULL)
    THEN RAISE(ABORT, 'non-terminal issue cannot retain outcome or closed_at') END;
  SELECT CASE WHEN (NEW.owner_id IS NULL) != (NEW.owner_kind IS NULL)
    OR (NEW.owner_id IS NULL) != (NEW.owner_claimed_at IS NULL)
    THEN RAISE(ABORT, 'issue owner fields must form a complete tuple') END;
END;

CREATE TRIGGER daemon_session_state_product_guard_insert
BEFORE INSERT ON daemon_session_projections
BEGIN
  SELECT CASE WHEN trim(NEW.project_id)='' OR trim(NEW.session_id)='' OR trim(NEW.issue_id)=''
    THEN RAISE(ABORT, 'session projection identity must be nonempty') END;
  SELECT CASE WHEN NEW.state NOT IN ('starting','running','attached','stopping','paused','stopped','idle','busy','waiting','done','error','unknown')
    THEN RAISE(ABORT, 'invalid desired session state') END;
  SELECT CASE WHEN NEW.observed_state IS NOT NULL AND NEW.observed_state NOT IN ('','starting','running','attached','stopping','paused','stopped','idle','busy','waiting','done','error','unknown')
    THEN RAISE(ABORT, 'invalid observed session state') END;
  SELECT CASE WHEN NEW.tmux_attached_count < 0
    THEN RAISE(ABORT, 'tmux attachment count cannot be negative') END;
  SELECT CASE WHEN (NEW.state='stopped' OR NEW.observed_state='stopped') AND NEW.tmux_attached_count != 0
    THEN RAISE(ABORT, 'stopped session cannot be attached') END;
END;

CREATE TRIGGER daemon_session_state_product_guard_update
BEFORE UPDATE ON daemon_session_projections
BEGIN
  SELECT CASE WHEN trim(NEW.project_id)='' OR trim(NEW.session_id)='' OR trim(NEW.issue_id)=''
    THEN RAISE(ABORT, 'session projection identity must be nonempty') END;
  SELECT CASE WHEN NEW.state NOT IN ('starting','running','attached','stopping','paused','stopped','idle','busy','waiting','done','error','unknown')
    THEN RAISE(ABORT, 'invalid desired session state') END;
  SELECT CASE WHEN NEW.observed_state IS NOT NULL AND NEW.observed_state NOT IN ('','starting','running','attached','stopping','paused','stopped','idle','busy','waiting','done','error','unknown')
    THEN RAISE(ABORT, 'invalid observed session state') END;
  SELECT CASE WHEN NEW.tmux_attached_count < 0
    THEN RAISE(ABORT, 'tmux attachment count cannot be negative') END;
  SELECT CASE WHEN (NEW.state='stopped' OR NEW.observed_state='stopped') AND NEW.tmux_attached_count != 0
    THEN RAISE(ABORT, 'stopped session cannot be attached') END;
END;

CREATE TRIGGER daemon_worktree_state_product_guard_insert
BEFORE INSERT ON daemon_worktree_projections
WHEN trim(NEW.project_id)='' OR trim(NEW.issue_id)='' OR trim(NEW.path)=''
BEGIN SELECT RAISE(ABORT, 'worktree projection identity and path must be nonempty'); END;

CREATE TRIGGER daemon_worktree_state_product_guard_update
BEFORE UPDATE ON daemon_worktree_projections
WHEN trim(NEW.project_id)='' OR trim(NEW.issue_id)='' OR trim(NEW.path)=''
BEGIN SELECT RAISE(ABORT, 'worktree projection identity and path must be nonempty'); END;

CREATE UNIQUE INDEX IF NOT EXISTS idx_daemon_worktree_projections_project_nonempty_path
  ON daemon_worktree_projections(project_id, path)
  WHERE trim(path) != '';
