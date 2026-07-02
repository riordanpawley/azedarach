CREATE TABLE IF NOT EXISTS agent_learning_tags (
	learning_id INTEGER NOT NULL,
	tag TEXT NOT NULL,
	tag_key TEXT NOT NULL,
	PRIMARY KEY (learning_id, tag_key),
	FOREIGN KEY (learning_id) REFERENCES agent_learnings(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_agent_learning_tags_key_learning
	ON agent_learning_tags(tag_key, learning_id);

CREATE TABLE IF NOT EXISTS agent_learning_files (
	learning_id INTEGER NOT NULL,
	file TEXT NOT NULL,
	file_key TEXT NOT NULL,
	PRIMARY KEY (learning_id, file_key),
	FOREIGN KEY (learning_id) REFERENCES agent_learnings(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_agent_learning_files_key_learning
	ON agent_learning_files(file_key, learning_id);

INSERT OR IGNORE INTO agent_learning_tags (learning_id, tag, tag_key)
SELECT l.id, trim(j.value), lower(trim(j.value))
FROM agent_learnings l,
	json_each(CASE WHEN json_valid(l.tags_json) THEN CASE WHEN json_type(l.tags_json) = 'array' THEN l.tags_json ELSE '[]' END ELSE '[]' END) AS j
WHERE typeof(j.value) = 'text'
	AND trim(j.value) != '';

INSERT OR IGNORE INTO agent_learning_files (learning_id, file, file_key)
SELECT l.id, trim(j.value), lower(trim(j.value))
FROM agent_learnings l,
	json_each(CASE WHEN json_valid(l.files_json) THEN CASE WHEN json_type(l.files_json) = 'array' THEN l.files_json ELSE '[]' END ELSE '[]' END) AS j
WHERE typeof(j.value) = 'text'
	AND trim(j.value) != '';

DROP TRIGGER IF EXISTS agent_learnings_ai_metadata;
DROP TRIGGER IF EXISTS agent_learnings_au_metadata;
DROP TRIGGER IF EXISTS agent_learnings_ad_metadata;

CREATE TRIGGER agent_learnings_ai_metadata
AFTER INSERT ON agent_learnings
BEGIN
	INSERT OR IGNORE INTO agent_learning_tags (learning_id, tag, tag_key)
	SELECT NEW.id, trim(j.value), lower(trim(j.value))
		FROM json_each(CASE WHEN json_valid(NEW.tags_json) THEN CASE WHEN json_type(NEW.tags_json) = 'array' THEN NEW.tags_json ELSE '[]' END ELSE '[]' END) AS j
	WHERE typeof(j.value) = 'text'
		AND trim(j.value) != '';

	INSERT OR IGNORE INTO agent_learning_files (learning_id, file, file_key)
	SELECT NEW.id, trim(j.value), lower(trim(j.value))
		FROM json_each(CASE WHEN json_valid(NEW.files_json) THEN CASE WHEN json_type(NEW.files_json) = 'array' THEN NEW.files_json ELSE '[]' END ELSE '[]' END) AS j
	WHERE typeof(j.value) = 'text'
		AND trim(j.value) != '';
END;

CREATE TRIGGER agent_learnings_au_metadata
AFTER UPDATE OF tags_json, files_json, deleted_at ON agent_learnings
BEGIN
	DELETE FROM agent_learning_tags WHERE learning_id = OLD.id;
	DELETE FROM agent_learning_files WHERE learning_id = OLD.id;

	INSERT OR IGNORE INTO agent_learning_tags (learning_id, tag, tag_key)
	SELECT NEW.id, trim(j.value), lower(trim(j.value))
	FROM json_each(CASE WHEN json_valid(NEW.tags_json) THEN CASE WHEN json_type(NEW.tags_json) = 'array' THEN NEW.tags_json ELSE '[]' END ELSE '[]' END) AS j
	WHERE typeof(j.value) = 'text'
		AND trim(j.value) != '';

	INSERT OR IGNORE INTO agent_learning_files (learning_id, file, file_key)
	SELECT NEW.id, trim(j.value), lower(trim(j.value))
	FROM json_each(CASE WHEN json_valid(NEW.files_json) THEN CASE WHEN json_type(NEW.files_json) = 'array' THEN NEW.files_json ELSE '[]' END ELSE '[]' END) AS j
	WHERE typeof(j.value) = 'text'
		AND trim(j.value) != '';
END;

CREATE TRIGGER agent_learnings_ad_metadata
AFTER DELETE ON agent_learnings
BEGIN
	DELETE FROM agent_learning_tags WHERE learning_id = OLD.id;
	DELETE FROM agent_learning_files WHERE learning_id = OLD.id;
END;
