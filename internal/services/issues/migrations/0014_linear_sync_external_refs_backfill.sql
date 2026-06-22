INSERT OR IGNORE INTO issue_external_refs (
	issue_id,
	provider,
	provider_scope,
	remote_key,
	display_key,
	url,
	created_at,
	updated_at,
	deleted_at
)
SELECT
	issue_id,
	provider,
	'',
	external_id,
	external_identifier,
	external_url,
	last_synced_at,
	last_synced_at,
	NULL
FROM azedarach_external_issue_refs
WHERE TRIM(COALESCE(provider, '')) <> ''
	AND TRIM(COALESCE(issue_id, '')) <> ''
	AND TRIM(COALESCE(external_id, '')) <> ''
	AND TRIM(COALESCE(external_identifier, '')) <> '';
