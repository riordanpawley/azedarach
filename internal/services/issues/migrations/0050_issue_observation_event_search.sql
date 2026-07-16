CREATE VIRTUAL TABLE issue_observation_event_search_fts USING fts5(
	issue_id UNINDEXED,
	content,
	content = '',
	detail = none,
	tokenize = 'unicode61'
);

CREATE TRIGGER issue_observation_events_ai_search_fts
AFTER INSERT ON issue_observation_events
BEGIN
	INSERT INTO issue_observation_event_search_fts(rowid, issue_id, content)
	VALUES (
		NEW.id,
		NEW.issue_id,
		COALESCE(json_extract(NEW.payload_json, '$.summary'), '') || ' ' ||
		COALESCE(json_extract(NEW.payload_json, '$.body'), '') || ' ' ||
		COALESCE(json_extract(NEW.payload_json, '$.message'), '') || ' ' ||
		COALESCE(json_extract(NEW.payload_json, '$.line'), '') || ' ' ||
		COALESCE(json_extract(NEW.payload_json, '$.evidence'), '')
	);
END;

CREATE TRIGGER issue_observation_events_ad_search_fts
AFTER DELETE ON issue_observation_events
BEGIN
	INSERT INTO issue_observation_event_search_fts(issue_observation_event_search_fts, rowid, issue_id, content)
	VALUES (
		'delete',
		OLD.id,
		OLD.issue_id,
		COALESCE(json_extract(OLD.payload_json, '$.summary'), '') || ' ' ||
		COALESCE(json_extract(OLD.payload_json, '$.body'), '') || ' ' ||
		COALESCE(json_extract(OLD.payload_json, '$.message'), '') || ' ' ||
		COALESCE(json_extract(OLD.payload_json, '$.line'), '') || ' ' ||
		COALESCE(json_extract(OLD.payload_json, '$.evidence'), '')
	);
END;

CREATE TRIGGER issue_observation_events_au_search_fts
AFTER UPDATE OF issue_id, payload_json ON issue_observation_events
BEGIN
	INSERT INTO issue_observation_event_search_fts(issue_observation_event_search_fts, rowid, issue_id, content)
	VALUES (
		'delete',
		OLD.id,
		OLD.issue_id,
		COALESCE(json_extract(OLD.payload_json, '$.summary'), '') || ' ' ||
		COALESCE(json_extract(OLD.payload_json, '$.body'), '') || ' ' ||
		COALESCE(json_extract(OLD.payload_json, '$.message'), '') || ' ' ||
		COALESCE(json_extract(OLD.payload_json, '$.line'), '') || ' ' ||
		COALESCE(json_extract(OLD.payload_json, '$.evidence'), '')
	);
	INSERT INTO issue_observation_event_search_fts(rowid, issue_id, content)
	VALUES (
		NEW.id,
		NEW.issue_id,
		COALESCE(json_extract(NEW.payload_json, '$.summary'), '') || ' ' ||
		COALESCE(json_extract(NEW.payload_json, '$.body'), '') || ' ' ||
		COALESCE(json_extract(NEW.payload_json, '$.message'), '') || ' ' ||
		COALESCE(json_extract(NEW.payload_json, '$.line'), '') || ' ' ||
		COALESCE(json_extract(NEW.payload_json, '$.evidence'), '')
	);
END;

INSERT INTO issue_observation_event_search_fts(rowid, issue_id, content)
SELECT
	id,
	issue_id,
	COALESCE(json_extract(payload_json, '$.summary'), '') || ' ' ||
	COALESCE(json_extract(payload_json, '$.body'), '') || ' ' ||
	COALESCE(json_extract(payload_json, '$.message'), '') || ' ' ||
	COALESCE(json_extract(payload_json, '$.line'), '') || ' ' ||
	COALESCE(json_extract(payload_json, '$.evidence'), '')
FROM issue_observation_events;

CREATE INDEX idx_issue_observation_events_issue_source_id
	ON issue_observation_events(issue_id, source, id);
CREATE INDEX idx_issue_observation_events_issue_source_command_id
	ON issue_observation_events(issue_id, source_command, id);
CREATE INDEX idx_issue_observation_events_issue_operation_id_id
	ON issue_observation_events(issue_id, operation_id, id);
CREATE INDEX idx_issue_observation_events_issue_session_id_id
	ON issue_observation_events(issue_id, session_id, id);
CREATE INDEX idx_issue_observation_events_issue_worktree_path_id
	ON issue_observation_events(issue_id, worktree_path, id);
CREATE INDEX idx_issue_observation_events_issue_payload_outcome_id
	ON issue_observation_events(issue_id, json_extract(payload_json, '$.outcome'), id);
CREATE INDEX idx_issue_observation_events_issue_payload_disposition_id
	ON issue_observation_events(issue_id, json_extract(payload_json, '$.disposition'), id);
CREATE INDEX idx_issue_observation_events_issue_payload_decision_id_id
	ON issue_observation_events(issue_id, json_extract(payload_json, '$.decision_id'), id);
CREATE INDEX idx_issue_observation_events_issue_payload_revision_id
	ON issue_observation_events(issue_id, json_extract(payload_json, '$.revision'), id);
CREATE INDEX idx_issue_observation_events_issue_payload_actor_id_id
	ON issue_observation_events(issue_id, json_extract(payload_json, '$.actor_id'), id);
