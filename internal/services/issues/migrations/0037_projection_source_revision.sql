CREATE TABLE projection_source_revision (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    revision INTEGER NOT NULL CHECK (revision >= 0)
);

-- Start the new logical clock above the legacy Unix-nanosecond checkpoint
-- domain. This preserves ordering for already-published user projections while
-- leaving more than four quintillion revisions before INTEGER overflow.
INSERT INTO projection_source_revision(singleton, revision) VALUES (1, 4611686018427387904);

CREATE TRIGGER projection_source_revision_issues_insert
AFTER INSERT ON issues BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;
CREATE TRIGGER projection_source_revision_issues_update
AFTER UPDATE ON issues BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;
CREATE TRIGGER projection_source_revision_issues_delete
AFTER DELETE ON issues BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;

CREATE TRIGGER projection_source_revision_dependencies_insert
AFTER INSERT ON issue_dependencies BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;
CREATE TRIGGER projection_source_revision_dependencies_update
AFTER UPDATE ON issue_dependencies BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;
CREATE TRIGGER projection_source_revision_dependencies_delete
AFTER DELETE ON issue_dependencies BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;

CREATE TRIGGER projection_source_revision_sessions_insert
AFTER INSERT ON daemon_session_projections BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;
CREATE TRIGGER projection_source_revision_sessions_update
AFTER UPDATE ON daemon_session_projections BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;
CREATE TRIGGER projection_source_revision_sessions_delete
AFTER DELETE ON daemon_session_projections BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;

CREATE TRIGGER projection_source_revision_session_observations_insert
AFTER INSERT ON daemon_session_observations BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;
CREATE TRIGGER projection_source_revision_session_observations_update
AFTER UPDATE ON daemon_session_observations BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;
CREATE TRIGGER projection_source_revision_session_observations_delete
AFTER DELETE ON daemon_session_observations BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;

CREATE TRIGGER projection_source_revision_worktrees_insert
AFTER INSERT ON daemon_worktree_projections BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;
CREATE TRIGGER projection_source_revision_worktrees_update
AFTER UPDATE ON daemon_worktree_projections BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;
CREATE TRIGGER projection_source_revision_worktrees_delete
AFTER DELETE ON daemon_worktree_projections BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;

CREATE TRIGGER projection_source_revision_external_refs_insert
AFTER INSERT ON issue_external_refs BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;
CREATE TRIGGER projection_source_revision_external_refs_update
AFTER UPDATE ON issue_external_refs BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;
CREATE TRIGGER projection_source_revision_external_refs_delete
AFTER DELETE ON issue_external_refs BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;

CREATE TRIGGER projection_source_revision_coordination_leases_insert
AFTER INSERT ON issue_coordination_leases BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;
CREATE TRIGGER projection_source_revision_coordination_leases_update
AFTER UPDATE ON issue_coordination_leases BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;
CREATE TRIGGER projection_source_revision_coordination_leases_delete
AFTER DELETE ON issue_coordination_leases BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;
