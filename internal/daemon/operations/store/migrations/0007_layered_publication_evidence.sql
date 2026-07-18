-- Authority: project.daemon_operations.
-- Schema: append-only layered publication evidence, explicit invalidations,
-- a revisioned project projection, and the durable sequential publication
-- queue that continues accepted patches into exact merge-result validation.
-- Data: existing validation requests remain unchanged; no legacy aggregate
-- request is fabricated into a stronger patch or merge-result identity.
-- Validation: startup verifies tables, indexes, immutability, and revision
-- triggers before serving evidence reads.
-- Ledger: the shared migration runner records this immutable artifact and its
-- pinned SHA-256 exactly once in the same transaction as the schema change.

CREATE TABLE daemon_publication_evidence (
    evidence_id TEXT PRIMARY KEY CHECK (length(evidence_id) > 0),
    project_id TEXT NOT NULL CHECK (length(project_id) > 0),
    issue_id TEXT NOT NULL CHECK (length(issue_id) > 0),
    layer TEXT NOT NULL CHECK (layer IN ('patch_review','active_path','merge_result')),
    patch_digest TEXT NOT NULL DEFAULT '',
    source_revision TEXT NOT NULL CHECK (length(source_revision) > 0),
    base_revision TEXT NOT NULL DEFAULT '',
    result_revision TEXT NOT NULL DEFAULT '',
    producer TEXT NOT NULL CHECK (length(producer) > 0),
    policy_version TEXT NOT NULL CHECK (length(policy_version) > 0),
    environment_fingerprint TEXT NOT NULL CHECK (length(environment_fingerprint) > 0),
    reused_from_evidence_id TEXT REFERENCES daemon_publication_evidence(evidence_id),
    coverage_json TEXT NOT NULL DEFAULT '{}',
    cost_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    CHECK (layer = 'merge_result' OR length(patch_digest) > 0),
    CHECK (layer != 'merge_result' OR (length(base_revision) > 0 AND length(result_revision) > 0)),
    CHECK (reused_from_evidence_id IS NULL OR reused_from_evidence_id != evidence_id)
);

CREATE TABLE daemon_publication_evidence_invalidations (
    invalidation_id TEXT PRIMARY KEY CHECK (length(invalidation_id) > 0),
    evidence_id TEXT NOT NULL REFERENCES daemon_publication_evidence(evidence_id),
    reason TEXT NOT NULL CHECK (reason IN (
        'source_changed','patch_changed','dirty_worktree','merge_conflict',
        'material_decision_changed','path_overlap','dependency_overlap',
        'high_risk_base_changed','toolchain_changed','policy_changed',
        'environment_changed','merge_input_changed','capability_absent','impact_unknown'
    )),
    details TEXT NOT NULL CHECK (length(details) > 0),
    created_at TEXT NOT NULL
);

CREATE TABLE daemon_publication_evidence_state (
    project_id TEXT PRIMARY KEY,
    revision INTEGER NOT NULL CHECK (revision > 0)
);

CREATE INDEX idx_daemon_publication_evidence_issue_layer
    ON daemon_publication_evidence(project_id, issue_id, layer, created_at);

CREATE INDEX idx_daemon_publication_evidence_invalidations
    ON daemon_publication_evidence_invalidations(evidence_id, created_at);

CREATE TRIGGER daemon_publication_evidence_immutable_update
BEFORE UPDATE ON daemon_publication_evidence
BEGIN SELECT RAISE(ABORT, 'publication evidence is immutable'); END;

CREATE TRIGGER daemon_publication_evidence_immutable_delete
BEFORE DELETE ON daemon_publication_evidence
BEGIN SELECT RAISE(ABORT, 'publication evidence is immutable'); END;

CREATE TRIGGER daemon_publication_invalidation_immutable_update
BEFORE UPDATE ON daemon_publication_evidence_invalidations
BEGIN SELECT RAISE(ABORT, 'publication evidence invalidation is immutable'); END;

CREATE TRIGGER daemon_publication_invalidation_immutable_delete
BEFORE DELETE ON daemon_publication_evidence_invalidations
BEGIN SELECT RAISE(ABORT, 'publication evidence invalidation is immutable'); END;

CREATE TRIGGER daemon_publication_evidence_insert_revision
AFTER INSERT ON daemon_publication_evidence
BEGIN
    INSERT INTO daemon_publication_evidence_state(project_id, revision) VALUES(NEW.project_id, 1)
    ON CONFLICT(project_id) DO UPDATE SET revision = revision + 1;
END;

CREATE TRIGGER daemon_publication_invalidation_insert_revision
AFTER INSERT ON daemon_publication_evidence_invalidations
BEGIN
    INSERT INTO daemon_publication_evidence_state(project_id, revision)
    SELECT project_id, 1 FROM daemon_publication_evidence WHERE evidence_id = NEW.evidence_id
    ON CONFLICT(project_id) DO UPDATE SET revision = revision + 1;
END;

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
    claim_token TEXT NOT NULL DEFAULT '',
    claim_expires_at INTEGER,
    validation_request_id TEXT NOT NULL DEFAULT '',
    reused_evidence_id TEXT NOT NULL DEFAULT '',
    failure_kind TEXT NOT NULL DEFAULT '',
    failure_detail TEXT NOT NULL DEFAULT '',
    failure_artifact TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    CHECK ((claim_token = '' AND claim_expires_at IS NULL) OR (lease_owner != '' AND claim_token != '' AND claim_expires_at IS NOT NULL)),
    UNIQUE(project_id, issue_id, intent_key),
    UNIQUE(project_id, coalesce_key)
);

CREATE INDEX idx_daemon_publication_operations_queue
ON daemon_publication_operations(project_id, target_branch, state, created_at, operation_id);

CREATE INDEX idx_daemon_publication_operations_issue
ON daemon_publication_operations(project_id, issue_id, created_at DESC);

CREATE INDEX idx_daemon_publication_operations_claim
ON daemon_publication_operations(state, claim_expires_at);

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
