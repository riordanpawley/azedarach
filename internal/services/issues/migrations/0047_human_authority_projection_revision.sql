-- Schema: make human-authority evidence part of the project projection clock.
-- Data: no issue, interaction, or observation rows are transformed.
-- Validation: startup requires all six canonical revision triggers after apply.
-- Ledger: the migration runner records this immutable artifact as
-- 0047_human_authority_projection_revision in the same transaction.

CREATE TRIGGER projection_source_revision_issue_observations_insert
AFTER INSERT ON issue_observation_events BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;
CREATE TRIGGER projection_source_revision_issue_observations_update
AFTER UPDATE ON issue_observation_events BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;
CREATE TRIGGER projection_source_revision_issue_observations_delete
AFTER DELETE ON issue_observation_events BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;

CREATE TRIGGER projection_source_revision_interactions_insert
AFTER INSERT ON interaction_requests BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;
CREATE TRIGGER projection_source_revision_interactions_update
AFTER UPDATE ON interaction_requests BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;
CREATE TRIGGER projection_source_revision_interactions_delete
AFTER DELETE ON interaction_requests BEGIN
    UPDATE projection_source_revision SET revision = revision + 1 WHERE singleton = 1;
END;
