CREATE TABLE IF NOT EXISTS issue_graph_closure (
	project_id TEXT NOT NULL DEFAULT 'default',
	ancestor_id TEXT NOT NULL,
	descendant_id TEXT NOT NULL,
	dependency_type TEXT NOT NULL,
	depth INTEGER NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (project_id, ancestor_id, descendant_id, dependency_type),
	FOREIGN KEY (ancestor_id) REFERENCES issues(id) ON DELETE CASCADE,
	FOREIGN KEY (descendant_id) REFERENCES issues(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_issue_graph_closure_ancestor
	ON issue_graph_closure(project_id, dependency_type, ancestor_id, depth, descendant_id);

CREATE INDEX IF NOT EXISTS idx_issue_graph_closure_descendant
	ON issue_graph_closure(project_id, dependency_type, descendant_id, depth, ancestor_id);

CREATE INDEX IF NOT EXISTS idx_issue_graph_closure_guard
	ON issue_graph_closure(project_id, dependency_type, ancestor_id, descendant_id);

DELETE FROM issue_graph_closure;

INSERT INTO issue_graph_closure (
	project_id,
	ancestor_id,
	descendant_id,
	dependency_type,
	depth,
	updated_at
)
WITH RECURSIVE parent_edges(ancestor_id, descendant_id) AS (
	SELECT d.depends_on_id, d.issue_id
	FROM issue_dependencies d
	INNER JOIN issues ancestor
		ON ancestor.id = d.depends_on_id
		AND ancestor.deleted_at IS NULL
	INNER JOIN issues descendant
		ON descendant.id = d.issue_id
		AND descendant.deleted_at IS NULL
	WHERE d.tombstoned_at IS NULL
		AND d.dependency_type IN ('parent-child', 'parent_child')
),
closure(ancestor_id, descendant_id, depth, path) AS (
	SELECT ancestor_id, descendant_id, 1, ',' || ancestor_id || ',' || descendant_id || ','
	FROM parent_edges
	UNION ALL
	SELECT c.ancestor_id, e.descendant_id, c.depth + 1, c.path || e.descendant_id || ','
	FROM closure c
	INNER JOIN parent_edges e
		ON e.ancestor_id = c.descendant_id
	WHERE instr(c.path, ',' || e.descendant_id || ',') = 0
)
SELECT
	'default',
	ancestor_id,
	descendant_id,
	'parent-child',
	MIN(depth),
	strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
FROM closure
WHERE ancestor_id <> descendant_id
GROUP BY ancestor_id, descendant_id;
