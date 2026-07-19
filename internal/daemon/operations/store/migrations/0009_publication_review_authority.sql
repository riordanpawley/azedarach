-- Authority: project.daemon_operations.
-- Schema: bind every durable publication operation to the exact orchestrator
-- review epoch, accepted-review event, and immutable patch-review evidence.
-- Data: no legacy operation is upgraded into stronger authority; existing rows
-- remain visibly unbound and fail closed if recovery encounters them.
-- Validation: startup verifies the columns and immutable identity trigger.
-- Ledger: the shared migration runner records this immutable artifact and its
-- pinned SHA-256 exactly once in the same transaction as the schema expansion.

ALTER TABLE daemon_publication_operations
ADD COLUMN reviewer_kind TEXT NOT NULL DEFAULT '';

ALTER TABLE daemon_publication_operations
ADD COLUMN review_epoch_event_id INTEGER NOT NULL DEFAULT 0
CHECK (review_epoch_event_id >= 0);

ALTER TABLE daemon_publication_operations
ADD COLUMN accepted_review_event_id INTEGER NOT NULL DEFAULT 0
CHECK (accepted_review_event_id >= 0);

ALTER TABLE daemon_publication_operations
ADD COLUMN patch_evidence_id TEXT NOT NULL DEFAULT '';

DROP TRIGGER daemon_publication_operation_identity_immutable;

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
