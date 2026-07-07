DROP TRIGGER IF EXISTS decisions_ai_search_fts;
DROP TRIGGER IF EXISTS decisions_au_search_fts;
DROP TRIGGER IF EXISTS decisions_ad_search_fts;
DROP TABLE IF EXISTS decision_search_fts;

CREATE VIRTUAL TABLE decision_search_fts USING fts5(
	local_id,
	title,
	rationale,
	context,
	consequences,
	content = '',
	detail = none,
	tokenize = 'unicode61'
);

CREATE TRIGGER decisions_ai_search_fts
AFTER INSERT ON decisions
BEGIN
	INSERT INTO decision_search_fts (
		rowid,
		local_id,
		title,
		rationale,
		context,
		consequences
	)
	VALUES (
		NEW.rowid,
		COALESCE(NEW.local_id, ''),
		COALESCE(NEW.title, ''),
		COALESCE(NEW.rationale, ''),
		COALESCE(NEW.context, ''),
		COALESCE(NEW.consequences, '')
	);
END;

CREATE TRIGGER decisions_au_search_fts
AFTER UPDATE ON decisions
BEGIN
	INSERT INTO decision_search_fts (
		decision_search_fts,
		rowid,
		local_id,
		title,
		rationale,
		context,
		consequences
	)
	SELECT
		'delete',
		OLD.rowid,
		COALESCE(OLD.local_id, ''),
		COALESCE(OLD.title, ''),
		COALESCE(OLD.rationale, ''),
		COALESCE(OLD.context, ''),
		COALESCE(OLD.consequences, '');

	INSERT INTO decision_search_fts (
		rowid,
		local_id,
		title,
		rationale,
		context,
		consequences
	)
	SELECT
		NEW.rowid,
		COALESCE(NEW.local_id, ''),
		COALESCE(NEW.title, ''),
		COALESCE(NEW.rationale, ''),
		COALESCE(NEW.context, ''),
		COALESCE(NEW.consequences, '');
END;

CREATE TRIGGER decisions_ad_search_fts
AFTER DELETE ON decisions
BEGIN
	INSERT INTO decision_search_fts (
		decision_search_fts,
		rowid,
		local_id,
		title,
		rationale,
		context,
		consequences
	)
	VALUES (
		'delete',
		OLD.rowid,
		COALESCE(OLD.local_id, ''),
		COALESCE(OLD.title, ''),
		COALESCE(OLD.rationale, ''),
		COALESCE(OLD.context, ''),
		COALESCE(OLD.consequences, '')
	);
END;

INSERT INTO decision_search_fts (
	rowid,
	local_id,
	title,
	rationale,
	context,
	consequences
)
SELECT
	rowid,
	COALESCE(local_id, ''),
	COALESCE(title, ''),
	COALESCE(rationale, ''),
	COALESCE(context, ''),
	COALESCE(consequences, '')
FROM decisions;
