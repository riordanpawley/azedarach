-- Schema: durable daemon-owned sequential publication queue.
-- Data: existing operations, validation requests, and publication evidence are unchanged.
-- Validation: immutable accepted-patch identity, typed states, unique intent/coalescing keys,
-- and deterministic target queue ordering are enforced by constraints and indexes.
-- Ledger: daemon_operations_0008_publication_queue is recorded by the migration registry.

CREATE TABLE daemon_publication_operations (
    operation_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    issue_id TEXT NOT NULL,
    intent_key TEXT NOT NULL,
    request_fingerprint TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    target_id TEXT NOT NULL CHECK (target_id = 'base'),
    target_branch TEXT NOT NULL,
    source_revision TEXT NOT NULL,
    base_revision TEXT NOT NULL,
    candidate_revision TEXT NOT NULL DEFAULT '',
    policy_version TEXT NOT NULL DEFAULT '',
    environment_fingerprint TEXT NOT NULL DEFAULT '',
    validation_command TEXT NOT NULL,
    evidence_source TEXT NOT NULL DEFAULT '',
    evidence_event_id INTEGER NOT NULL DEFAULT 0 CHECK (evidence_event_id >= 0),
    evidence_seq INTEGER NOT NULL DEFAULT 0 CHECK (evidence_seq >= 0),
    evidence_digest TEXT NOT NULL DEFAULT '',
    coalesce_key TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('queued','preparing','validating','passed','failed','conflicted','stale','merged','canceled')),
    lease_owner TEXT NOT NULL DEFAULT '',
    validation_request_id TEXT NOT NULL DEFAULT '',
    reused_evidence_id TEXT NOT NULL DEFAULT '',
    failure_kind TEXT NOT NULL DEFAULT '',
    failure_detail TEXT NOT NULL DEFAULT '',
    failure_artifact TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    UNIQUE(project_id, issue_id, intent_key),
    UNIQUE(project_id, coalesce_key)
);

CREATE INDEX idx_daemon_publication_operations_queue
ON daemon_publication_operations(project_id, target_branch, state, created_at, operation_id);

CREATE INDEX idx_daemon_publication_operations_issue
ON daemon_publication_operations(project_id, issue_id, created_at DESC);

CREATE TRIGGER daemon_publication_operation_identity_immutable
BEFORE UPDATE ON daemon_publication_operations
WHEN NEW.operation_id != OLD.operation_id
  OR NEW.project_id != OLD.project_id
  OR NEW.issue_id != OLD.issue_id
  OR NEW.intent_key != OLD.intent_key
  OR NEW.request_fingerprint != OLD.request_fingerprint
  OR NEW.actor_id != OLD.actor_id
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
