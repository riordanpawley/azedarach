CREATE TABLE IF NOT EXISTS daemon_session_projections (
	project_id TEXT NOT NULL,
	session_id TEXT NOT NULL,
	issue_id TEXT NOT NULL,
	state TEXT NOT NULL,
	started_at TEXT,
	updated_at TEXT NOT NULL,
	tmux_attached_count INTEGER NOT NULL DEFAULT 0,
	observed_state TEXT,
	activity TEXT,
	activity_source TEXT,
	PRIMARY KEY (project_id, session_id)
);

CREATE INDEX IF NOT EXISTS idx_daemon_session_projections_project_issue
	ON daemon_session_projections (project_id, issue_id);

CREATE TABLE IF NOT EXISTS daemon_worktree_projections (
	project_id TEXT NOT NULL,
	issue_id TEXT NOT NULL,
	path TEXT NOT NULL,
	branch TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	git_status_json TEXT,
	git_status_updated_at TEXT,
	PRIMARY KEY (project_id, issue_id)
);

CREATE INDEX IF NOT EXISTS idx_daemon_worktree_projections_project_path
	ON daemon_worktree_projections (project_id, path);

DROP TABLE IF EXISTS temp.closed_runtime_invariant_repairs;

CREATE TEMP TABLE closed_runtime_invariant_repairs (
	issue_id TEXT PRIMARY KEY
);

INSERT OR IGNORE INTO closed_runtime_invariant_repairs (issue_id)
SELECT i.id
FROM issues i
WHERE
	i.status = 'closed'
	AND i.deleted_at IS NULL
	AND (
		EXISTS (
			SELECT 1
			FROM daemon_worktree_projections w
			WHERE
				w.issue_id = i.id
				AND TRIM(COALESCE(w.path, '')) <> ''
		)
		OR EXISTS (
			SELECT 1
			FROM daemon_session_projections s
			WHERE
				s.issue_id = i.id
				AND LOWER(TRIM(COALESCE(s.state, ''))) <> 'stopped'
		)
	);

WITH RECURSIVE descendants(root_id, issue_id) AS (
	SELECT parent.id, d.issue_id
	FROM issues parent
	JOIN issue_dependencies d ON d.depends_on_id = parent.id
	WHERE
		parent.status = 'closed'
		AND parent.deleted_at IS NULL
		AND d.tombstoned_at IS NULL
		AND d.dependency_type IN ('parent-child', 'parent_child')
	UNION
	SELECT descendants.root_id, d.issue_id
	FROM descendants
	JOIN issue_dependencies d ON d.depends_on_id = descendants.issue_id
	WHERE
		d.tombstoned_at IS NULL
		AND d.dependency_type IN ('parent-child', 'parent_child')
)
INSERT OR IGNORE INTO closed_runtime_invariant_repairs (issue_id)
SELECT DISTINCT descendants.root_id
FROM descendants
JOIN issues child ON child.id = descendants.issue_id
WHERE
	child.deleted_at IS NULL
	AND (
		child.status <> 'closed'
		OR
		EXISTS (
			SELECT 1
			FROM daemon_worktree_projections w
			WHERE
				w.issue_id = child.id
				AND TRIM(COALESCE(w.path, '')) <> ''
		)
		OR EXISTS (
			SELECT 1
			FROM daemon_session_projections s
			WHERE
				s.issue_id = child.id
				AND LOWER(TRIM(COALESCE(s.state, ''))) <> 'stopped'
		)
	);

UPDATE issues
SET
	status = 'in_review',
	closed_at = NULL,
	updated_at = STRFTIME('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id IN (
	SELECT issue_id
	FROM closed_runtime_invariant_repairs
);

DROP TABLE temp.closed_runtime_invariant_repairs;

CREATE TRIGGER IF NOT EXISTS issue_closed_runtime_guard_insert
BEFORE INSERT ON issues
WHEN NEW.status = 'closed'
BEGIN
	SELECT RAISE(ABORT, 'closed issue cannot have active runtime attachments')
	WHERE
		EXISTS (
			SELECT 1
			FROM daemon_worktree_projections w
			WHERE
				w.issue_id = NEW.id
				AND TRIM(COALESCE(w.path, '')) <> ''
		)
		OR EXISTS (
			SELECT 1
			FROM daemon_session_projections s
			WHERE
				s.issue_id = NEW.id
				AND LOWER(TRIM(COALESCE(s.state, ''))) <> 'stopped'
		)
		OR EXISTS (
			WITH RECURSIVE descendants(issue_id) AS (
				SELECT d.issue_id
				FROM issue_dependencies d
				WHERE
					d.depends_on_id = NEW.id
					AND d.tombstoned_at IS NULL
					AND d.dependency_type IN ('parent-child', 'parent_child')
				UNION
				SELECT d.issue_id
				FROM descendants
				JOIN issue_dependencies d ON d.depends_on_id = descendants.issue_id
				WHERE
					d.tombstoned_at IS NULL
					AND d.dependency_type IN ('parent-child', 'parent_child')
			)
			SELECT 1
			FROM descendants
			JOIN issues child ON child.id = descendants.issue_id
			WHERE
				child.deleted_at IS NULL
				AND (
					child.status <> 'closed'
					OR
					EXISTS (
						SELECT 1
						FROM daemon_worktree_projections w
						WHERE
							w.issue_id = child.id
							AND TRIM(COALESCE(w.path, '')) <> ''
					)
					OR EXISTS (
						SELECT 1
						FROM daemon_session_projections s
						WHERE
							s.issue_id = child.id
							AND LOWER(TRIM(COALESCE(s.state, ''))) <> 'stopped'
					)
				)
		);
END;

CREATE TRIGGER IF NOT EXISTS issue_closed_runtime_guard_update
BEFORE UPDATE OF status ON issues
WHEN NEW.status = 'closed'
BEGIN
	SELECT RAISE(ABORT, 'closed issue cannot have active runtime attachments')
	WHERE
		EXISTS (
			SELECT 1
			FROM daemon_worktree_projections w
			WHERE
				w.issue_id = NEW.id
				AND TRIM(COALESCE(w.path, '')) <> ''
		)
		OR EXISTS (
			SELECT 1
			FROM daemon_session_projections s
			WHERE
				s.issue_id = NEW.id
				AND LOWER(TRIM(COALESCE(s.state, ''))) <> 'stopped'
		)
		OR EXISTS (
			WITH RECURSIVE descendants(issue_id) AS (
				SELECT d.issue_id
				FROM issue_dependencies d
				WHERE
					d.depends_on_id = NEW.id
					AND d.tombstoned_at IS NULL
					AND d.dependency_type IN ('parent-child', 'parent_child')
				UNION
				SELECT d.issue_id
				FROM descendants
				JOIN issue_dependencies d ON d.depends_on_id = descendants.issue_id
				WHERE
					d.tombstoned_at IS NULL
					AND d.dependency_type IN ('parent-child', 'parent_child')
			)
			SELECT 1
			FROM descendants
			JOIN issues child ON child.id = descendants.issue_id
			WHERE
				child.deleted_at IS NULL
				AND (
					child.status <> 'closed'
					OR
					EXISTS (
						SELECT 1
						FROM daemon_worktree_projections w
						WHERE
							w.issue_id = child.id
							AND TRIM(COALESCE(w.path, '')) <> ''
					)
					OR EXISTS (
						SELECT 1
						FROM daemon_session_projections s
						WHERE
							s.issue_id = child.id
							AND LOWER(TRIM(COALESCE(s.state, ''))) <> 'stopped'
					)
				)
		);
END;

CREATE TRIGGER IF NOT EXISTS daemon_worktree_closed_issue_guard_insert
BEFORE INSERT ON daemon_worktree_projections
WHEN TRIM(COALESCE(NEW.path, '')) <> ''
BEGIN
	SELECT RAISE(ABORT, 'cannot attach worktree to closed issue or closed ancestor')
	WHERE EXISTS (
		WITH RECURSIVE ancestors(issue_id) AS (
			SELECT NEW.issue_id
			UNION
			SELECT d.depends_on_id
			FROM ancestors
			JOIN issue_dependencies d ON d.issue_id = ancestors.issue_id
			WHERE
				d.tombstoned_at IS NULL
				AND d.dependency_type IN ('parent-child', 'parent_child')
		)
		SELECT 1
		FROM ancestors
		JOIN issues i ON i.id = ancestors.issue_id
		WHERE
			i.status = 'closed'
			AND i.deleted_at IS NULL
	);
END;

CREATE TRIGGER IF NOT EXISTS daemon_worktree_closed_issue_guard_update
BEFORE UPDATE OF issue_id, path ON daemon_worktree_projections
WHEN TRIM(COALESCE(NEW.path, '')) <> ''
BEGIN
	SELECT RAISE(ABORT, 'cannot attach worktree to closed issue or closed ancestor')
	WHERE EXISTS (
		WITH RECURSIVE ancestors(issue_id) AS (
			SELECT NEW.issue_id
			UNION
			SELECT d.depends_on_id
			FROM ancestors
			JOIN issue_dependencies d ON d.issue_id = ancestors.issue_id
			WHERE
				d.tombstoned_at IS NULL
				AND d.dependency_type IN ('parent-child', 'parent_child')
		)
		SELECT 1
		FROM ancestors
		JOIN issues i ON i.id = ancestors.issue_id
		WHERE
			i.status = 'closed'
			AND i.deleted_at IS NULL
	);
END;

CREATE TRIGGER IF NOT EXISTS daemon_session_closed_issue_guard_insert
BEFORE INSERT ON daemon_session_projections
WHEN LOWER(TRIM(COALESCE(NEW.state, ''))) <> 'stopped'
BEGIN
	SELECT RAISE(ABORT, 'cannot attach active session to closed issue or closed ancestor')
	WHERE EXISTS (
		WITH RECURSIVE ancestors(issue_id) AS (
			SELECT NEW.issue_id
			UNION
			SELECT d.depends_on_id
			FROM ancestors
			JOIN issue_dependencies d ON d.issue_id = ancestors.issue_id
			WHERE
				d.tombstoned_at IS NULL
				AND d.dependency_type IN ('parent-child', 'parent_child')
		)
		SELECT 1
		FROM ancestors
		JOIN issues i ON i.id = ancestors.issue_id
		WHERE
			i.status = 'closed'
			AND i.deleted_at IS NULL
	);
END;

CREATE TRIGGER IF NOT EXISTS daemon_session_closed_issue_guard_update
BEFORE UPDATE OF issue_id, state ON daemon_session_projections
WHEN LOWER(TRIM(COALESCE(NEW.state, ''))) <> 'stopped'
BEGIN
	SELECT RAISE(ABORT, 'cannot attach active session to closed issue or closed ancestor')
	WHERE EXISTS (
		WITH RECURSIVE ancestors(issue_id) AS (
			SELECT NEW.issue_id
			UNION
			SELECT d.depends_on_id
			FROM ancestors
			JOIN issue_dependencies d ON d.issue_id = ancestors.issue_id
			WHERE
				d.tombstoned_at IS NULL
				AND d.dependency_type IN ('parent-child', 'parent_child')
		)
		SELECT 1
		FROM ancestors
		JOIN issues i ON i.id = ancestors.issue_id
		WHERE
			i.status = 'closed'
			AND i.deleted_at IS NULL
	);
END;

CREATE TRIGGER IF NOT EXISTS issue_dependency_closed_runtime_guard_insert
BEFORE INSERT ON issue_dependencies
WHEN
	NEW.tombstoned_at IS NULL
	AND NEW.dependency_type IN ('parent-child', 'parent_child')
BEGIN
	SELECT RAISE(ABORT, 'cannot place unresolved descendant under closed issue')
	WHERE
		EXISTS (
			WITH RECURSIVE ancestors(issue_id) AS (
				SELECT NEW.depends_on_id
				UNION
				SELECT d.depends_on_id
				FROM ancestors
				JOIN issue_dependencies d ON d.issue_id = ancestors.issue_id
				WHERE
					d.tombstoned_at IS NULL
					AND d.dependency_type IN ('parent-child', 'parent_child')
			)
			SELECT 1
			FROM ancestors
			JOIN issues i ON i.id = ancestors.issue_id
			WHERE
				i.status = 'closed'
				AND i.deleted_at IS NULL
		)
		AND EXISTS (
			WITH RECURSIVE descendants(issue_id) AS (
				SELECT NEW.issue_id
				UNION
				SELECT d.issue_id
				FROM descendants
				JOIN issue_dependencies d ON d.depends_on_id = descendants.issue_id
				WHERE
					d.tombstoned_at IS NULL
					AND d.dependency_type IN ('parent-child', 'parent_child')
			)
			SELECT 1
			FROM descendants
			LEFT JOIN issues child ON child.id = descendants.issue_id
			WHERE
				(
					child.id IS NOT NULL
					AND child.deleted_at IS NULL
					AND child.status <> 'closed'
				)
				OR
				EXISTS (
					SELECT 1
					FROM daemon_worktree_projections w
					WHERE
						w.issue_id = descendants.issue_id
						AND TRIM(COALESCE(w.path, '')) <> ''
				)
				OR EXISTS (
					SELECT 1
					FROM daemon_session_projections s
					WHERE
						s.issue_id = descendants.issue_id
						AND LOWER(TRIM(COALESCE(s.state, ''))) <> 'stopped'
				)
		);
END;

CREATE TRIGGER IF NOT EXISTS issue_dependency_closed_runtime_guard_update
BEFORE UPDATE OF issue_id, depends_on_id, dependency_type, tombstoned_at ON issue_dependencies
WHEN
	NEW.tombstoned_at IS NULL
	AND NEW.dependency_type IN ('parent-child', 'parent_child')
BEGIN
	SELECT RAISE(ABORT, 'cannot place unresolved descendant under closed issue')
	WHERE
		EXISTS (
			WITH RECURSIVE ancestors(issue_id) AS (
				SELECT NEW.depends_on_id
				UNION
				SELECT d.depends_on_id
				FROM ancestors
				JOIN issue_dependencies d ON d.issue_id = ancestors.issue_id
				WHERE
					d.tombstoned_at IS NULL
					AND d.dependency_type IN ('parent-child', 'parent_child')
			)
			SELECT 1
			FROM ancestors
			JOIN issues i ON i.id = ancestors.issue_id
			WHERE
				i.status = 'closed'
				AND i.deleted_at IS NULL
		)
		AND EXISTS (
			WITH RECURSIVE descendants(issue_id) AS (
				SELECT NEW.issue_id
				UNION
				SELECT d.issue_id
				FROM descendants
				JOIN issue_dependencies d ON d.depends_on_id = descendants.issue_id
				WHERE
					d.tombstoned_at IS NULL
					AND d.dependency_type IN ('parent-child', 'parent_child')
			)
			SELECT 1
			FROM descendants
			LEFT JOIN issues child ON child.id = descendants.issue_id
			WHERE
				(
					child.id IS NOT NULL
					AND child.deleted_at IS NULL
					AND child.status <> 'closed'
				)
				OR
				EXISTS (
					SELECT 1
					FROM daemon_worktree_projections w
					WHERE
						w.issue_id = descendants.issue_id
						AND TRIM(COALESCE(w.path, '')) <> ''
				)
				OR EXISTS (
					SELECT 1
					FROM daemon_session_projections s
					WHERE
						s.issue_id = descendants.issue_id
						AND LOWER(TRIM(COALESCE(s.state, ''))) <> 'stopped'
				)
		);
END;

CREATE TRIGGER IF NOT EXISTS issue_descendant_closed_ancestor_guard_update
BEFORE UPDATE OF status, deleted_at ON issues
WHEN NEW.status <> 'closed' AND NEW.deleted_at IS NULL
BEGIN
	SELECT RAISE(ABORT, 'cannot move descendant out of closed under closed ancestor')
	WHERE EXISTS (
		WITH RECURSIVE ancestors(issue_id) AS (
			SELECT d.depends_on_id
			FROM issue_dependencies d
			WHERE
				d.issue_id = NEW.id
				AND d.tombstoned_at IS NULL
				AND d.dependency_type IN ('parent-child', 'parent_child')
			UNION
			SELECT d.depends_on_id
			FROM ancestors
			JOIN issue_dependencies d ON d.issue_id = ancestors.issue_id
			WHERE
				d.tombstoned_at IS NULL
				AND d.dependency_type IN ('parent-child', 'parent_child')
		)
		SELECT 1
		FROM ancestors
		JOIN issues i ON i.id = ancestors.issue_id
		WHERE
			i.status = 'closed'
			AND i.deleted_at IS NULL
	);
END;
