-- Authority: project.daemon_operations.
-- Schema: append-only layered publication evidence, explicit invalidations,
-- and a revisioned project projection for coherent multi-daemon reads.
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
