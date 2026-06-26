CREATE TABLE IF NOT EXISTS issue_attachments (
	issue_id TEXT NOT NULL,
	attachment_id TEXT NOT NULL,
	filename TEXT NOT NULL,
	relative_path TEXT NOT NULL,
	mime_type TEXT NOT NULL,
	size INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	PRIMARY KEY (issue_id, attachment_id)
);

CREATE INDEX IF NOT EXISTS idx_issue_attachments_attachment_id
	ON issue_attachments(attachment_id);
