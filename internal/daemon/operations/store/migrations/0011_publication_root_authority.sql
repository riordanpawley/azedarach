-- Authority: project.daemon_operations.
-- Schema: retain the original accepted publication operation independently
-- from each retry operation and from immutable patch-review evidence.
-- Data: fail closed by binding each legacy row to itself. The migration
-- runner then performs the data-dependent trusted-event backfill in this same
-- transaction when the project issue_observation_events authority exists. It
-- accepts only daemon-orchestration/review-accept/accepted events whose root
-- operation agrees on issue, reviewer, epoch, accepted event, and source.
-- Validation: startup verifies the composed columns and immutable trigger.
-- Ledger: the shared runner records this artifact checksum transactionally.

ALTER TABLE daemon_publication_operations
ADD COLUMN accepted_publication_operation_id TEXT NOT NULL DEFAULT '';

UPDATE daemon_publication_operations
SET accepted_publication_operation_id = operation_id;

DROP TRIGGER daemon_publication_operation_identity_immutable;

-- GO_ASSISTED_BACKFILL_BOUNDARY: the runner completes trusted-event backfill
-- here, before reinstalling the immutable identity guard.
CREATE TRIGGER daemon_publication_operation_identity_immutable
BEFORE UPDATE ON daemon_publication_operations
WHEN NEW.operation_id != OLD.operation_id
  OR NEW.project_id != OLD.project_id
  OR NEW.issue_id != OLD.issue_id
  OR NEW.intent_key != OLD.intent_key
  OR NEW.request_fingerprint != OLD.request_fingerprint
  OR NEW.actor_id != OLD.actor_id
  OR NEW.reviewer_kind != OLD.reviewer_kind
  OR NEW.review_epoch_event_id != OLD.review_epoch_event_id
  OR (OLD.accepted_review_event_id != 0 AND NEW.accepted_review_event_id != OLD.accepted_review_event_id)
  OR NEW.patch_evidence_id != OLD.patch_evidence_id
  OR NEW.accepted_publication_operation_id != OLD.accepted_publication_operation_id
  OR NEW.target_id != OLD.target_id
  OR NEW.target_branch != OLD.target_branch
  OR NEW.source_revision != OLD.source_revision
  OR NEW.base_revision != OLD.base_revision
  OR NEW.policy_version != OLD.policy_version
  OR NEW.environment_fingerprint != OLD.environment_fingerprint
  OR NEW.validation_command != OLD.validation_command
  OR NEW.evidence_source != OLD.evidence_source
  OR NEW.evidence_event_id != OLD.evidence_event_id
  OR NEW.evidence_seq != OLD.evidence_seq
  OR NEW.evidence_digest != OLD.evidence_digest
  OR NEW.coalesce_key != OLD.coalesce_key
  OR NEW.created_at != OLD.created_at
BEGIN
    SELECT RAISE(ABORT, 'publication operation identity is immutable');
END;
