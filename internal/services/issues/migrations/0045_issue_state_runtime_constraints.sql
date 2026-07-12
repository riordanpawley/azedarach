-- Persistent guards for local state-product invariants. Cross-module facts
-- (notably tmux presence) remain daemon hybrid invariants.

INSERT INTO meta(key,value)
SELECT 'issue:canonical_state_v3_archive_adapter_cutover',
       json_object('cleared_equal_archive_delete_mirrors',COUNT(*),'recorded_at',strftime('%Y-%m-%dT%H:%M:%fZ','now'))
FROM issues WHERE deleted_at IS NOT NULL AND deleted_at=archived_at
ON CONFLICT(key) DO UPDATE SET value=excluded.value;
UPDATE issues SET deleted_at=NULL
WHERE deleted_at IS NOT NULL AND deleted_at=archived_at;

CREATE TABLE IF NOT EXISTS issue_runtime_divergences (
  issue_id TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('lifecycle_runtime')),
  reason TEXT NOT NULL,
  detected_at TEXT NOT NULL,
  resolved_at TEXT,
  PRIMARY KEY(issue_id,kind),
  FOREIGN KEY(issue_id) REFERENCES issues(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_issue_runtime_divergences_active
  ON issue_runtime_divergences(issue_id,detected_at) WHERE resolved_at IS NULL;

CREATE TABLE issue_state_runtime_constraint_validation (
  ok INTEGER NOT NULL CHECK (ok = 1)
);
INSERT INTO issue_state_runtime_constraint_validation(ok)
SELECT 0 WHERE EXISTS (
  SELECT 1 FROM issues WHERE
	 COALESCE(disposition,'') NOT IN ('backlog','ready','completed','cancelled') OR
	 COALESCE(engagement,'') NOT IN ('idle','working','review_requested') OR
	 COALESCE(visibility,'') NOT IN ('live','archived') OR
	 (disposition!='ready' AND engagement!='idle') OR
	 (visibility='archived' AND engagement!='idle') OR
	 deleted_at IS NOT NULL OR
	 (visibility='live' AND archived_at IS NOT NULL) OR
	 (visibility='archived' AND archived_at IS NULL) OR
    issue_type NOT IN ('task','bug','feature','epic','chore','investigation') OR
    lifecycle_state NOT IN ('backlog','open','active','closed') OR
    review_state NOT IN ('none','requested') OR
    closed_outcome NOT IN ('none','completed','cancelled') OR
    (review_state='requested' AND lifecycle_state!='active') OR
    (lifecycle_state='closed' AND (closed_outcome='none' OR closed_at IS NULL)) OR
    (lifecycle_state!='closed' AND (closed_outcome!='none' OR closed_at IS NOT NULL))
);
INSERT INTO issue_state_runtime_constraint_validation(ok)
SELECT 0 WHERE EXISTS (
  -- This projection is issue-scoped. Advisor/project/root sessions live in the
  -- typed daemon runtime-state tables and are not represented here.
  SELECT 1 FROM daemon_session_projections WHERE
    trim(project_id)='' OR trim(session_id)='' OR trim(issue_id)='' OR
    role NOT IN ('worker','advisor','orchestrator') OR scope_kind NOT IN ('issue','interaction','orchestration') OR
    (role='worker' AND (scope_kind!='issue' OR NOT EXISTS(SELECT 1 FROM issues i WHERE i.id=daemon_session_projections.issue_id))) OR
    (role='advisor' AND (scope_kind!='interaction' OR trim(scope_id)='')) OR
    (role='orchestrator' AND (scope_kind!='orchestration' OR trim(scope_id)='')) OR
    state NOT IN ('starting','running','attached','stopping','paused','stopped','idle','busy','waiting','done','error','unknown') OR
    (observed_state IS NOT NULL AND observed_state NOT IN ('','starting','running','attached','stopping','paused','stopped','idle','busy','waiting','done','error','unknown')) OR
    tmux_attached_count < 0 OR
    ((state='stopped' OR observed_state='stopped') AND tmux_attached_count != 0)
);
INSERT INTO issue_state_runtime_constraint_validation(ok)
SELECT 0 WHERE EXISTS (
  SELECT 1 FROM daemon_worktree_projections WHERE trim(project_id)='' OR trim(issue_id)='' OR trim(path)='' OR
    NOT EXISTS(SELECT 1 FROM issues i WHERE i.id=daemon_worktree_projections.issue_id)
);
INSERT INTO issue_state_runtime_constraint_validation(ok)
SELECT 0 WHERE EXISTS (
  SELECT 1 FROM issue_coordination_leases l LEFT JOIN issues i ON i.id=l.issue_id WHERE
    i.id IS NULL OR i.visibility!='live' OR i.disposition IN ('completed','cancelled') OR
    (l.purpose='execution' AND i.disposition!='ready') OR
    (l.purpose='review' AND (i.disposition!='ready' OR i.engagement!='review_requested')) OR
    (l.purpose='orchestration' AND i.disposition NOT IN ('backlog','ready'))
);
DROP TABLE issue_state_runtime_constraint_validation;

CREATE TRIGGER issue_state_product_guard_insert
BEFORE INSERT ON issues
BEGIN
  SELECT CASE WHEN COALESCE(NEW.disposition,'') NOT IN ('backlog','ready','completed','cancelled') THEN RAISE(ABORT, 'invalid disposition') END;
  SELECT CASE WHEN COALESCE(NEW.engagement,'') NOT IN ('idle','working','review_requested') THEN RAISE(ABORT, 'invalid engagement') END;
  SELECT CASE WHEN COALESCE(NEW.visibility,'') NOT IN ('live','archived') THEN RAISE(ABORT, 'invalid visibility') END;
  SELECT CASE WHEN NEW.disposition!='ready' AND NEW.engagement!='idle' THEN RAISE(ABORT, 'non-ready issue requires idle engagement') END;
  SELECT CASE WHEN NEW.visibility='archived' AND NEW.engagement!='idle' THEN RAISE(ABORT, 'archived issue requires idle engagement') END;
  SELECT CASE WHEN NEW.deleted_at IS NOT NULL THEN RAISE(ABORT, 'issue deletion timestamp is not canonical authority') END;
  SELECT CASE WHEN (NEW.visibility='live') != (NEW.archived_at IS NULL) THEN RAISE(ABORT, 'visibility/archive audit mismatch') END;
  SELECT CASE WHEN NEW.lifecycle_state != CASE WHEN NEW.disposition='backlog' THEN 'backlog' WHEN NEW.disposition='ready' AND NEW.engagement='idle' THEN 'open' WHEN NEW.disposition='ready' THEN 'active' ELSE 'closed' END THEN RAISE(ABORT, 'legacy lifecycle projection mismatch') END;
  SELECT CASE WHEN NEW.review_state != CASE WHEN NEW.engagement='review_requested' THEN 'requested' ELSE 'none' END THEN RAISE(ABORT, 'legacy review projection mismatch') END;
  SELECT CASE WHEN NEW.closed_outcome != CASE WHEN NEW.disposition='completed' THEN 'completed' WHEN NEW.disposition='cancelled' THEN 'cancelled' ELSE 'none' END THEN RAISE(ABORT, 'legacy outcome projection mismatch') END;
  SELECT CASE WHEN NEW.status != CASE WHEN NEW.disposition='completed' THEN 'closed' WHEN NEW.disposition='cancelled' THEN 'cancelled' WHEN NEW.engagement='review_requested' THEN 'in_review' WHEN NEW.engagement='working' THEN 'in_progress' ELSE 'open' END THEN RAISE(ABORT, 'legacy status projection mismatch') END;
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
  SELECT CASE WHEN EXISTS(SELECT 1 FROM issue_coordination_leases l WHERE l.issue_id=NEW.id AND NOT (
    NEW.visibility='live' AND NEW.disposition NOT IN ('completed','cancelled') AND
    ((l.purpose='execution' AND NEW.disposition='ready') OR
     (l.purpose='review' AND NEW.disposition='ready' AND NEW.engagement='review_requested') OR
     (l.purpose='orchestration' AND NEW.disposition IN ('backlog','ready')))))
    THEN RAISE(ABORT, 'issue state is ineligible for existing lease purpose') END;
END;

CREATE TRIGGER issue_state_product_guard_update
BEFORE UPDATE ON issues
BEGIN
  SELECT CASE WHEN COALESCE(NEW.disposition,'') NOT IN ('backlog','ready','completed','cancelled') THEN RAISE(ABORT, 'invalid disposition') END;
  SELECT CASE WHEN COALESCE(NEW.engagement,'') NOT IN ('idle','working','review_requested') THEN RAISE(ABORT, 'invalid engagement') END;
  SELECT CASE WHEN COALESCE(NEW.visibility,'') NOT IN ('live','archived') THEN RAISE(ABORT, 'invalid visibility') END;
  SELECT CASE WHEN NEW.disposition!='ready' AND NEW.engagement!='idle' THEN RAISE(ABORT, 'non-ready issue requires idle engagement') END;
  SELECT CASE WHEN NEW.visibility='archived' AND NEW.engagement!='idle' THEN RAISE(ABORT, 'archived issue requires idle engagement') END;
  SELECT CASE WHEN NEW.deleted_at IS NOT NULL THEN RAISE(ABORT, 'issue deletion timestamp is not canonical authority') END;
  SELECT CASE WHEN (NEW.visibility='live') != (NEW.archived_at IS NULL) THEN RAISE(ABORT, 'visibility/archive audit mismatch') END;
  SELECT CASE WHEN NEW.lifecycle_state != CASE WHEN NEW.disposition='backlog' THEN 'backlog' WHEN NEW.disposition='ready' AND NEW.engagement='idle' THEN 'open' WHEN NEW.disposition='ready' THEN 'active' ELSE 'closed' END THEN RAISE(ABORT, 'legacy lifecycle projection mismatch') END;
  SELECT CASE WHEN NEW.review_state != CASE WHEN NEW.engagement='review_requested' THEN 'requested' ELSE 'none' END THEN RAISE(ABORT, 'legacy review projection mismatch') END;
  SELECT CASE WHEN NEW.closed_outcome != CASE WHEN NEW.disposition='completed' THEN 'completed' WHEN NEW.disposition='cancelled' THEN 'cancelled' ELSE 'none' END THEN RAISE(ABORT, 'legacy outcome projection mismatch') END;
  SELECT CASE WHEN NEW.status != CASE WHEN NEW.disposition='completed' THEN 'closed' WHEN NEW.disposition='cancelled' THEN 'cancelled' WHEN NEW.engagement='review_requested' THEN 'in_review' WHEN NEW.engagement='working' THEN 'in_progress' ELSE 'open' END THEN RAISE(ABORT, 'legacy status projection mismatch') END;
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
  SELECT CASE WHEN EXISTS(SELECT 1 FROM issue_coordination_leases l WHERE l.issue_id=NEW.id AND NOT (
    NEW.visibility='live' AND NEW.disposition NOT IN ('completed','cancelled') AND
    ((l.purpose='execution' AND NEW.disposition='ready') OR
     (l.purpose='review' AND NEW.disposition='ready' AND NEW.engagement='review_requested') OR
     (l.purpose='orchestration' AND NEW.disposition IN ('backlog','ready')))))
    THEN RAISE(ABORT, 'issue state is ineligible for existing lease purpose') END;
END;

CREATE TRIGGER issue_archive_aggregate_guard
BEFORE UPDATE OF visibility ON issues
WHEN NEW.visibility='archived'
BEGIN
  SELECT CASE WHEN EXISTS(SELECT 1 FROM issue_coordination_leases WHERE issue_id=NEW.id)
    THEN RAISE(ABORT, 'archived issue cannot retain claims') END;
  SELECT CASE WHEN EXISTS(SELECT 1 FROM daemon_worktree_projections WHERE issue_id=NEW.id AND trim(path)!='')
    THEN RAISE(ABORT, 'archived issue cannot retain worktree') END;
  SELECT CASE WHEN EXISTS(SELECT 1 FROM daemon_session_projections WHERE issue_id=NEW.id)
    THEN RAISE(ABORT, 'archived issue cannot retain session') END;
END;

CREATE TRIGGER issue_lease_archived_guard
BEFORE INSERT ON issue_coordination_leases
WHEN NOT EXISTS(SELECT 1 FROM issues i WHERE i.id=NEW.issue_id AND i.visibility='live' AND i.disposition NOT IN ('completed','cancelled') AND
  ((NEW.purpose='execution' AND i.disposition='ready') OR
   (NEW.purpose='review' AND i.disposition='ready' AND i.engagement='review_requested') OR
   (NEW.purpose='orchestration' AND i.disposition IN ('backlog','ready'))))
BEGIN SELECT RAISE(ABORT, 'issue state is ineligible for lease purpose'); END;

CREATE TRIGGER issue_lease_state_guard_update
BEFORE UPDATE ON issue_coordination_leases
WHEN NOT EXISTS(SELECT 1 FROM issues i WHERE i.id=NEW.issue_id AND i.visibility='live' AND i.disposition NOT IN ('completed','cancelled') AND
  ((NEW.purpose='execution' AND i.disposition='ready') OR
   (NEW.purpose='review' AND i.disposition='ready' AND i.engagement='review_requested') OR
   (NEW.purpose='orchestration' AND i.disposition IN ('backlog','ready'))))
BEGIN SELECT RAISE(ABORT, 'issue state is ineligible for lease purpose'); END;

CREATE TRIGGER issue_worktree_archived_guard
BEFORE INSERT ON daemon_worktree_projections
WHEN EXISTS(SELECT 1 FROM issues WHERE id=NEW.issue_id AND visibility='archived')
BEGIN SELECT RAISE(ABORT, 'cannot attach worktree to archived issue'); END;

CREATE TRIGGER issue_worktree_archived_guard_update
BEFORE UPDATE ON daemon_worktree_projections
WHEN EXISTS(SELECT 1 FROM issues WHERE id=NEW.issue_id AND visibility='archived')
BEGIN SELECT RAISE(ABORT, 'cannot attach worktree to archived issue'); END;

CREATE TRIGGER issue_session_archived_guard
BEFORE INSERT ON daemon_session_projections
WHEN EXISTS(SELECT 1 FROM issues WHERE id=NEW.issue_id AND visibility='archived')
BEGIN SELECT RAISE(ABORT, 'cannot attach session to archived issue'); END;

CREATE TRIGGER issue_session_archived_guard_update
BEFORE UPDATE ON daemon_session_projections
WHEN EXISTS(SELECT 1 FROM issues WHERE id=NEW.issue_id AND visibility='archived')
BEGIN SELECT RAISE(ABORT, 'cannot attach session to archived issue'); END;

CREATE TRIGGER daemon_session_state_product_guard_insert
BEFORE INSERT ON daemon_session_projections
BEGIN
  SELECT CASE WHEN trim(NEW.project_id)='' OR trim(NEW.session_id)='' OR trim(NEW.issue_id)=''
    THEN RAISE(ABORT, 'session projection identity must be nonempty') END;
  SELECT CASE WHEN NEW.role NOT IN ('worker','advisor','orchestrator') OR NEW.scope_kind NOT IN ('issue','interaction','orchestration')
    THEN RAISE(ABORT, 'invalid session role or scope') END;
  SELECT CASE WHEN NEW.role='worker' AND (NEW.scope_kind!='issue' OR NOT EXISTS(SELECT 1 FROM issues WHERE id=NEW.issue_id))
    THEN RAISE(ABORT, 'worker session projection requires existing issue scope') END;
  SELECT CASE WHEN NEW.role='advisor' AND (NEW.scope_kind!='interaction' OR trim(NEW.scope_id)='')
    THEN RAISE(ABORT, 'advisor session requires interaction scope') END;
  SELECT CASE WHEN NEW.role='orchestrator' AND (NEW.scope_kind!='orchestration' OR trim(NEW.scope_id)='')
    THEN RAISE(ABORT, 'orchestrator session requires orchestration scope') END;
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
  SELECT CASE WHEN NEW.role NOT IN ('worker','advisor','orchestrator') OR NEW.scope_kind NOT IN ('issue','interaction','orchestration')
    THEN RAISE(ABORT, 'invalid session role or scope') END;
  SELECT CASE WHEN NEW.role='worker' AND (NEW.scope_kind!='issue' OR NOT EXISTS(SELECT 1 FROM issues WHERE id=NEW.issue_id))
    THEN RAISE(ABORT, 'worker session projection requires existing issue scope') END;
  SELECT CASE WHEN NEW.role='advisor' AND (NEW.scope_kind!='interaction' OR trim(NEW.scope_id)='')
    THEN RAISE(ABORT, 'advisor session requires interaction scope') END;
  SELECT CASE WHEN NEW.role='orchestrator' AND (NEW.scope_kind!='orchestration' OR trim(NEW.scope_id)='')
    THEN RAISE(ABORT, 'orchestrator session requires orchestration scope') END;
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
WHEN trim(NEW.project_id)='' OR trim(NEW.issue_id)='' OR trim(NEW.path)='' OR NOT EXISTS(SELECT 1 FROM issues WHERE id=NEW.issue_id)
BEGIN SELECT RAISE(ABORT, 'worktree projection requires nonempty identity, path, and existing issue'); END;

CREATE TRIGGER daemon_worktree_state_product_guard_update
BEFORE UPDATE ON daemon_worktree_projections
WHEN trim(NEW.project_id)='' OR trim(NEW.issue_id)='' OR trim(NEW.path)='' OR NOT EXISTS(SELECT 1 FROM issues WHERE id=NEW.issue_id)
BEGIN SELECT RAISE(ABORT, 'worktree projection requires nonempty identity, path, and existing issue'); END;

CREATE UNIQUE INDEX IF NOT EXISTS idx_daemon_worktree_projections_project_nonempty_path
  ON daemon_worktree_projections(project_id, path)
  WHERE trim(path) != '';
