-- Authority: project.daemon_operations.
-- Schema: persist daemon-authoritative ticket priority and the bounded number
-- of later higher-priority starts that have overtaken each capacity request.
-- Data: existing requests receive neutral P2 priority and zero bypass debt.
-- Validation: startup verifies both constrained columns and the priority queue
-- index used by the transactional admission reconciler.
-- Ledger: the shared migration runner records this immutable artifact and its
-- pinned SHA-256 exactly once in the same transaction as the schema expansion.

ALTER TABLE daemon_validation_requests
ADD COLUMN issue_priority INTEGER NOT NULL DEFAULT 2
CHECK (issue_priority BETWEEN 0 AND 4);

ALTER TABLE daemon_validation_requests
ADD COLUMN priority_bypass_count INTEGER NOT NULL DEFAULT 0
CHECK (priority_bypass_count >= 0);

CREATE INDEX idx_daemon_validation_priority_queue
    ON daemon_validation_requests(
        project_id,
        state,
        purpose,
        priority_bypass_count,
        issue_priority,
        sequence
    );
