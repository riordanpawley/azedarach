CREATE INDEX IF NOT EXISTS idx_issues_deleted_updated ON issues(deleted_at, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_issues_status_deleted_priority_updated ON issues(status, deleted_at, priority, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_dependencies_issue_active_type ON issue_dependencies(issue_id, tombstoned_at, dependency_type);
CREATE INDEX IF NOT EXISTS idx_dependencies_depends_on_active_type ON issue_dependencies(depends_on_id, tombstoned_at, dependency_type);

-- Keep compatibility with existing ts-opentui query paths/index names.
CREATE INDEX IF NOT EXISTS idx_dependencies_depends_on ON issue_dependencies(depends_on_id, dependency_type, tombstoned_at);
