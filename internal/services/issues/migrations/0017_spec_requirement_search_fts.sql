DROP TRIGGER IF EXISTS spec_requirements_ai_search_fts;
DROP TRIGGER IF EXISTS spec_requirements_au_search_fts;
DROP TRIGGER IF EXISTS spec_requirements_ad_search_fts;
DROP TABLE IF EXISTS spec_requirement_search_fts;

CREATE VIRTUAL TABLE spec_requirement_search_fts USING fts5(
	local_id,
	external_code,
	title,
	description,
	status,
	content = '',
	detail = none,
	tokenize = 'unicode61'
);

CREATE TRIGGER spec_requirements_ai_search_fts
AFTER INSERT ON spec_requirements
WHEN NEW.deleted_at IS NULL
BEGIN
	INSERT INTO spec_requirement_search_fts (
		rowid,
		local_id,
		external_code,
		title,
		description,
		status
	)
	VALUES (
		NEW.rowid,
		COALESCE(NEW.local_id, ''),
		COALESCE(NEW.external_code, ''),
		COALESCE(NEW.title, ''),
		COALESCE(NEW.description, ''),
		COALESCE(NEW.status, '')
	);
END;

CREATE TRIGGER spec_requirements_au_search_fts
AFTER UPDATE ON spec_requirements
BEGIN
	INSERT INTO spec_requirement_search_fts (
		spec_requirement_search_fts,
		rowid,
		local_id,
		external_code,
		title,
		description,
		status
	)
	SELECT
		'delete',
		OLD.rowid,
		COALESCE(OLD.local_id, ''),
		COALESCE(OLD.external_code, ''),
		COALESCE(OLD.title, ''),
		COALESCE(OLD.description, ''),
		COALESCE(OLD.status, '')
	WHERE OLD.deleted_at IS NULL;

	INSERT INTO spec_requirement_search_fts (
		rowid,
		local_id,
		external_code,
		title,
		description,
		status
	)
	SELECT
		NEW.rowid,
		COALESCE(NEW.local_id, ''),
		COALESCE(NEW.external_code, ''),
		COALESCE(NEW.title, ''),
		COALESCE(NEW.description, ''),
		COALESCE(NEW.status, '')
	WHERE NEW.deleted_at IS NULL;
END;

CREATE TRIGGER spec_requirements_ad_search_fts
AFTER DELETE ON spec_requirements
WHEN OLD.deleted_at IS NULL
BEGIN
	INSERT INTO spec_requirement_search_fts (
		spec_requirement_search_fts,
		rowid,
		local_id,
		external_code,
		title,
		description,
		status
	)
	VALUES (
		'delete',
		OLD.rowid,
		COALESCE(OLD.local_id, ''),
		COALESCE(OLD.external_code, ''),
		COALESCE(OLD.title, ''),
		COALESCE(OLD.description, ''),
		COALESCE(OLD.status, '')
	);
END;

INSERT INTO spec_requirement_search_fts (
	rowid,
	local_id,
	external_code,
	title,
	description,
	status
)
SELECT
	rowid,
	COALESCE(local_id, ''),
	COALESCE(external_code, ''),
	COALESCE(title, ''),
	COALESCE(description, ''),
	COALESCE(status, '')
FROM spec_requirements
WHERE deleted_at IS NULL;
