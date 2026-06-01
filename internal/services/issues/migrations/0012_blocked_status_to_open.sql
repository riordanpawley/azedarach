DROP TABLE IF EXISTS temp.blocked_status_migration_map;

CREATE TEMP TABLE blocked_status_migration_map (
	issue_id TEXT PRIMARY KEY,
	blocker_id TEXT NOT NULL UNIQUE
);

WITH RECURSIVE blocker_candidates(issue_id, blocker_id, suffix, available) AS (
	SELECT
		id,
		id || '-legacy-blocker',
		0,
		NOT EXISTS (
			SELECT 1
			FROM issues existing
			WHERE existing.id = issues.id || '-legacy-blocker'
		)
	FROM issues
	WHERE status = 'blocked'
		AND deleted_at IS NULL
	UNION ALL
	SELECT
		issue_id,
		issue_id || '-legacy-blocker-' || (suffix + 1),
		suffix + 1,
		NOT EXISTS (
			SELECT 1
			FROM issues existing
			WHERE existing.id = blocker_candidates.issue_id || '-legacy-blocker-' || (blocker_candidates.suffix + 1)
		)
	FROM blocker_candidates
	WHERE available = 0
)
INSERT INTO blocked_status_migration_map (issue_id, blocker_id)
SELECT issue_id, blocker_id
FROM blocker_candidates
WHERE available = 1;

INSERT INTO issues (
	id,
	title,
	description,
	status,
	priority,
	issue_type,
	created_at,
	updated_at,
	labels_json,
	implementations_json,
	deleted_at
)
SELECT
	m.blocker_id,
	'Resolve blocker for ' || i.id,
	'Created during blocked status migration so this issue remains graph-blocked until the blocker is closed.',
	'open',
	i.priority,
	'task',
	i.updated_at,
	i.updated_at,
	'[]',
	'[]',
	NULL
FROM blocked_status_migration_map m
JOIN issues i ON i.id = m.issue_id;

INSERT INTO issue_dependencies (
	issue_id,
	depends_on_id,
	dependency_type,
	tombstoned_at
)
SELECT
	issue_id,
	blocker_id,
	'blocks',
	NULL
FROM blocked_status_migration_map;

UPDATE issues
SET status = 'open'
WHERE status = 'blocked';

DROP TABLE temp.blocked_status_migration_map;
