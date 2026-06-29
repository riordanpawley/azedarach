DROP TRIGGER IF EXISTS issues_ai_search_fts;
DROP TRIGGER IF EXISTS issues_au_search_fts;
DROP TRIGGER IF EXISTS issues_ad_search_fts;
DROP TABLE IF EXISTS issue_search_fts;

CREATE VIRTUAL TABLE issue_search_fts USING fts5(
	issue_id,
	title,
	description,
	notes,
	design,
	acceptance,
	assignee,
	labels,
	implementations,
	status,
	priority,
	issue_type,
	content = '',
	detail = none,
	tokenize = 'unicode61'
);

CREATE TRIGGER issues_ai_search_fts
AFTER INSERT ON issues
WHEN NEW.deleted_at IS NULL
BEGIN
	INSERT INTO issue_search_fts (
		rowid,
		issue_id,
		title,
		description,
		notes,
		design,
		acceptance,
		assignee,
		labels,
		implementations,
		status,
		priority,
		issue_type
	)
	VALUES (
		NEW.rowid,
		NEW.id,
		COALESCE(NEW.title, ''),
		COALESCE(NEW.description, ''),
		COALESCE(NEW.notes, ''),
		COALESCE(NEW.design, ''),
		COALESCE(NEW.acceptance, ''),
		COALESCE(NEW.assignee, ''),
		COALESCE(NEW.labels_json, ''),
		COALESCE(NEW.implementations_json, ''),
		COALESCE(NEW.status, ''),
		CASE COALESCE(NEW.priority, 2)
			WHEN 0 THEN 'P0'
			WHEN 1 THEN 'P1'
			WHEN 2 THEN 'P2'
			WHEN 3 THEN 'P3'
			WHEN 4 THEN 'P4'
			ELSE 'P' || COALESCE(NEW.priority, 2)
		END,
		COALESCE(NEW.issue_type, '')
	);
END;

CREATE TRIGGER issues_au_search_fts
AFTER UPDATE ON issues
BEGIN
	INSERT INTO issue_search_fts (
		issue_search_fts,
		rowid,
		issue_id,
		title,
		description,
		notes,
		design,
		acceptance,
		assignee,
		labels,
		implementations,
		status,
		priority,
		issue_type
	)
	SELECT
		'delete',
		OLD.rowid,
		OLD.id,
		COALESCE(OLD.title, ''),
		COALESCE(OLD.description, ''),
		COALESCE(OLD.notes, ''),
		COALESCE(OLD.design, ''),
		COALESCE(OLD.acceptance, ''),
		COALESCE(OLD.assignee, ''),
		COALESCE(OLD.labels_json, ''),
		COALESCE(OLD.implementations_json, ''),
		COALESCE(OLD.status, ''),
		CASE COALESCE(OLD.priority, 2)
			WHEN 0 THEN 'P0'
			WHEN 1 THEN 'P1'
			WHEN 2 THEN 'P2'
			WHEN 3 THEN 'P3'
			WHEN 4 THEN 'P4'
			ELSE 'P' || COALESCE(OLD.priority, 2)
		END,
		COALESCE(OLD.issue_type, '')
	WHERE OLD.deleted_at IS NULL;

	INSERT INTO issue_search_fts (
		rowid,
		issue_id,
		title,
		description,
		notes,
		design,
		acceptance,
		assignee,
		labels,
		implementations,
		status,
		priority,
		issue_type
	)
	SELECT
		NEW.rowid,
		NEW.id,
		COALESCE(NEW.title, ''),
		COALESCE(NEW.description, ''),
		COALESCE(NEW.notes, ''),
		COALESCE(NEW.design, ''),
		COALESCE(NEW.acceptance, ''),
		COALESCE(NEW.assignee, ''),
		COALESCE(NEW.labels_json, ''),
		COALESCE(NEW.implementations_json, ''),
		COALESCE(NEW.status, ''),
		CASE COALESCE(NEW.priority, 2)
			WHEN 0 THEN 'P0'
			WHEN 1 THEN 'P1'
			WHEN 2 THEN 'P2'
			WHEN 3 THEN 'P3'
			WHEN 4 THEN 'P4'
			ELSE 'P' || COALESCE(NEW.priority, 2)
		END,
		COALESCE(NEW.issue_type, '')
	WHERE NEW.deleted_at IS NULL;
END;

CREATE TRIGGER issues_ad_search_fts
AFTER DELETE ON issues
WHEN OLD.deleted_at IS NULL
BEGIN
	INSERT INTO issue_search_fts (
		issue_search_fts,
		rowid,
		issue_id,
		title,
		description,
		notes,
		design,
		acceptance,
		assignee,
		labels,
		implementations,
		status,
		priority,
		issue_type
	)
	VALUES (
		'delete',
		OLD.rowid,
		OLD.id,
		COALESCE(OLD.title, ''),
		COALESCE(OLD.description, ''),
		COALESCE(OLD.notes, ''),
		COALESCE(OLD.design, ''),
		COALESCE(OLD.acceptance, ''),
		COALESCE(OLD.assignee, ''),
		COALESCE(OLD.labels_json, ''),
		COALESCE(OLD.implementations_json, ''),
		COALESCE(OLD.status, ''),
		CASE COALESCE(OLD.priority, 2)
			WHEN 0 THEN 'P0'
			WHEN 1 THEN 'P1'
			WHEN 2 THEN 'P2'
			WHEN 3 THEN 'P3'
			WHEN 4 THEN 'P4'
			ELSE 'P' || COALESCE(OLD.priority, 2)
		END,
		COALESCE(OLD.issue_type, '')
	);
END;

INSERT INTO issue_search_fts (
	rowid,
	issue_id,
	title,
	description,
	notes,
	design,
	acceptance,
	assignee,
	labels,
	implementations,
	status,
	priority,
	issue_type
)
SELECT
	rowid,
	id,
	COALESCE(title, ''),
	COALESCE(description, ''),
	COALESCE(notes, ''),
	COALESCE(design, ''),
	COALESCE(acceptance, ''),
	COALESCE(assignee, ''),
	COALESCE(labels_json, ''),
	COALESCE(implementations_json, ''),
	COALESCE(status, ''),
	CASE COALESCE(priority, 2)
		WHEN 0 THEN 'P0'
		WHEN 1 THEN 'P1'
		WHEN 2 THEN 'P2'
		WHEN 3 THEN 'P3'
		WHEN 4 THEN 'P4'
		ELSE 'P' || COALESCE(priority, 2)
	END,
	COALESCE(issue_type, '')
FROM issues
WHERE deleted_at IS NULL;
