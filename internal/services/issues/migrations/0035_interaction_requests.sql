CREATE TABLE interaction_requests (
    id TEXT PRIMARY KEY,
    issue_id TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    decision_key TEXT NOT NULL,
    state TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    request_json TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX idx_interaction_requests_issue_state
    ON interaction_requests(issue_id, state, updated_at DESC);
CREATE INDEX idx_interaction_requests_scope_state
    ON interaction_requests(state, updated_at DESC);
CREATE UNIQUE INDEX idx_interaction_requests_unresolved_decision
    ON interaction_requests(issue_id, decision_key)
    WHERE state IN ('open', 'discussing', 'answer_proposed');
