CREATE TABLE issue_dependencies_new (
	issue_id TEXT NOT NULL,
	depends_on_id TEXT NOT NULL,
	dependency_type TEXT NOT NULL,
	tombstoned_at TEXT,
	PRIMARY KEY (issue_id, depends_on_id, dependency_type),
	FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE,
	FOREIGN KEY (depends_on_id) REFERENCES issues(id) ON DELETE CASCADE
);

INSERT INTO issue_dependencies_new (issue_id, depends_on_id, dependency_type, tombstoned_at)
	SELECT d.issue_id, d.depends_on_id, d.dependency_type, d.tombstoned_at
	FROM issue_dependencies d
	INNER JOIN issues src ON src.id = d.issue_id
	INNER JOIN issues dep ON dep.id = d.depends_on_id;

DROP TABLE issue_dependencies;
ALTER TABLE issue_dependencies_new RENAME TO issue_dependencies;
