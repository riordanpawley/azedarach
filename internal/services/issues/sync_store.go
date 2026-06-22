package issues

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

// ExternalRef records the durable mapping between a local issue and an issue in
// an external tracker.
type ExternalRef struct {
	Provider           string    `json:"provider"`
	IssueID            string    `json:"issue_id"`
	ExternalID         string    `json:"external_id"`
	ExternalIdentifier string    `json:"external_identifier"`
	ExternalURL        string    `json:"external_url,omitempty"`
	ExternalUpdatedAt  time.Time `json:"external_updated_at,omitempty"`
	LastSyncedAt       time.Time `json:"last_synced_at"`
	LastSyncHash       string    `json:"last_sync_hash"`
	LastSyncPayload    string    `json:"last_sync_payload,omitempty"`
}

// SyncConflict captures an unresolved field-level external sync conflict.
type SyncConflict struct {
	ID              string     `json:"id"`
	Provider        string     `json:"provider"`
	ProjectID       string     `json:"project_id"`
	IssueID         string     `json:"issue_id"`
	Field           string     `json:"field"`
	LocalValue      string     `json:"local_value,omitempty"`
	RemoteValue     string     `json:"remote_value,omitempty"`
	LocalUpdatedAt  *time.Time `json:"local_updated_at,omitempty"`
	RemoteUpdatedAt *time.Time `json:"remote_updated_at,omitempty"`
	DetectedAt      time.Time  `json:"detected_at"`
	ResolvedAt      *time.Time `json:"resolved_at,omitempty"`
}

// ExternalSyncState stores per-provider/per-scope cursor state for efficient
// incremental external tracker sync.
type ExternalSyncState struct {
	Provider      string    `json:"provider"`
	ProjectID     string    `json:"project_id"`
	Cursor        string    `json:"cursor,omitempty"`
	LastSuccessAt time.Time `json:"last_success_at,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// UpsertSyncedTask inserts or updates a local issue using an externally-owned
// identifier. It is intentionally narrow and used by sync import paths only.
func (c *Client) UpsertSyncedTask(ctx context.Context, task domain.Task) (bool, error) {
	db, err := c.dbHandle()
	if err != nil {
		return false, err
	}
	issueID := strings.TrimSpace(task.ID.String())
	if issueID == "" {
		return false, c.wrapError("sync-upsert", issueID, errors.New("issue id is required"))
	}
	if strings.TrimSpace(task.Title) == "" {
		return false, c.wrapError("sync-upsert", issueID, errors.New("title is required"))
	}
	if task.Status == "" {
		task.Status = domain.StatusOpen
	}
	if task.Type == "" {
		task.Type = domain.TypeTask
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now().UTC()
	}
	if task.UpdatedAt.IsZero() {
		task.UpdatedAt = task.CreatedAt
	}
	labelsJSON, err := marshalOptionalStringSlice(task.Labels)
	if err != nil {
		return false, c.wrapError("sync-upsert", issueID, err)
	}
	implsJSON, err := marshalOptionalStringSlice(task.Implementations)
	if err != nil {
		return false, c.wrapError("sync-upsert", issueID, err)
	}
	var estimate any
	if task.Estimate != nil {
		estimate = *task.Estimate
	}
	var closedAt any
	if task.Status == domain.StatusDone {
		closedAt = task.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	res, err := db.ExecContext(ctx, `
		INSERT INTO issues (
			id, title, description, status, priority, issue_type,
			created_at, updated_at, closed_at, assignee, labels_json,
			implementations_json, design, notes, acceptance, estimate, deleted_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
		ON CONFLICT(id) DO UPDATE SET
			title = excluded.title,
			description = excluded.description,
			status = excluded.status,
			priority = excluded.priority,
			issue_type = excluded.issue_type,
			updated_at = excluded.updated_at,
			closed_at = excluded.closed_at,
			assignee = excluded.assignee,
			labels_json = excluded.labels_json,
			implementations_json = excluded.implementations_json,
			deleted_at = NULL
	`, issueID, task.Title, nullableString(task.Description), string(task.Status), int(task.Priority), string(task.Type), task.CreatedAt.UTC().Format(time.RFC3339Nano), task.UpdatedAt.UTC().Format(time.RFC3339Nano), closedAt, nullableString(task.Assignee), labelsJSON, implsJSON, nullableString(task.Design), nullableString(task.Notes), nullableString(task.Acceptance), estimate)
	if err != nil {
		return false, c.wrapError("sync-upsert", issueID, err)
	}
	affected, _ := res.RowsAffected()
	return affected > 0, nil
}

func (c *Client) UpsertExternalRef(ctx context.Context, ref ExternalRef) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	ref.Provider = strings.TrimSpace(ref.Provider)
	ref.IssueID = strings.TrimSpace(ref.IssueID)
	ref.ExternalID = strings.TrimSpace(ref.ExternalID)
	ref.ExternalIdentifier = strings.TrimSpace(ref.ExternalIdentifier)
	if ref.Provider == "" || ref.IssueID == "" || ref.ExternalID == "" || ref.ExternalIdentifier == "" {
		return c.wrapError("external-ref-upsert", ref.IssueID, errors.New("provider, issue id, external id, and external identifier are required"))
	}
	if ref.LastSyncedAt.IsZero() {
		ref.LastSyncedAt = time.Now().UTC()
	}
	if ref.LastSyncHash == "" {
		ref.LastSyncHash = HashTaskForSync(domain.Task{ID: naming.IssueID(ref.IssueID)})
	}
	providerScope, metadata, err := c.linearSyncExternalRefTarget(ctx, db, ref)
	if err != nil {
		return c.wrapError("external-ref-upsert", ref.IssueID, err)
	}
	applyExternalRefSyncMetadata(metadata, ref)
	metadataJSON, err := marshalStringMap(metadata)
	if err != nil {
		return c.wrapError("external-ref-upsert", ref.IssueID, err)
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO issue_external_refs (
			issue_id, provider, provider_scope, remote_key, display_key, url,
			metadata_json, created_at, updated_at, deleted_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
		ON CONFLICT(issue_id, provider, provider_scope, remote_key) DO UPDATE SET
			display_key = excluded.display_key,
			url = excluded.url,
			metadata_json = excluded.metadata_json,
			updated_at = excluded.updated_at,
			deleted_at = NULL
	`, ref.IssueID, ref.Provider, providerScope, ref.ExternalID, nullableString(ref.ExternalIdentifier), nullableString(ref.ExternalURL), nullableString(metadataJSON), ref.LastSyncedAt.UTC().Format(time.RFC3339Nano), ref.LastSyncedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return c.wrapError("external-ref-upsert", ref.IssueID, err)
	}
	return nil
}

func (c *Client) ListExternalRefs(ctx context.Context, provider string) ([]ExternalRef, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT
			r.provider,
			r.issue_id,
			r.remote_key,
			COALESCE(r.display_key, ''),
			COALESCE(r.url, ''),
			COALESCE(r.metadata_json, ''),
			r.updated_at,
			COALESCE(a.external_updated_at, ''),
			COALESCE(a.last_synced_at, ''),
			COALESCE(a.last_sync_hash, ''),
			COALESCE(a.last_sync_payload, ''),
			COALESCE(i.title, ''),
			COALESCE(i.description, ''),
			COALESCE(i.status, ''),
			COALESCE(i.priority, 0),
			COALESCE(i.issue_type, ''),
			COALESCE(i.assignee, ''),
			COALESCE(i.labels_json, '[]')
		FROM issue_external_refs r
		INNER JOIN issues i ON i.id = r.issue_id AND i.deleted_at IS NULL
		LEFT JOIN azedarach_external_issue_refs a
			ON a.provider = r.provider AND a.issue_id = r.issue_id
		WHERE r.provider = ? AND r.deleted_at IS NULL
	`, strings.TrimSpace(provider))
	if err != nil {
		return nil, c.wrapError("external-ref-list", provider, err)
	}
	defer rows.Close()
	refs := []ExternalRef{}
	for rows.Next() {
		var ref ExternalRef
		var metadataRaw, rowUpdatedRaw string
		var legacyExternalUpdatedRaw, legacyLastSyncedRaw, legacyHash, legacyPayload string
		var task domain.Task
		var statusRaw, typeRaw, labelsRaw string
		var priorityRaw int
		if err := rows.Scan(
			&ref.Provider,
			&ref.IssueID,
			&ref.ExternalID,
			&ref.ExternalIdentifier,
			&ref.ExternalURL,
			&metadataRaw,
			&rowUpdatedRaw,
			&legacyExternalUpdatedRaw,
			&legacyLastSyncedRaw,
			&legacyHash,
			&legacyPayload,
			&task.Title,
			&task.Description,
			&statusRaw,
			&priorityRaw,
			&typeRaw,
			&task.Assignee,
			&labelsRaw,
		); err != nil {
			return nil, err
		}
		metadata := map[string]string{}
		if strings.TrimSpace(metadataRaw) != "" {
			if err := json.Unmarshal([]byte(metadataRaw), &metadata); err != nil {
				return nil, c.wrapError("external-ref-list", ref.IssueID, err)
			}
		}
		ref.ExternalUpdatedAt = parseTimestamp(firstNonEmpty(metadata[externalRefMetadataExternalUpdatedAt], legacyExternalUpdatedRaw))
		ref.LastSyncedAt = parseTimestamp(firstNonEmpty(metadata[externalRefMetadataLastSyncedAt], legacyLastSyncedRaw, rowUpdatedRaw))
		ref.LastSyncHash = firstNonEmpty(metadata[externalRefMetadataLastSyncHash], legacyHash)
		ref.LastSyncPayload = firstNonEmpty(metadata[externalRefMetadataLastSyncPayload], legacyPayload)
		if ref.ExternalIdentifier == "" {
			ref.ExternalIdentifier = ref.ExternalID
		}
		if ref.LastSyncHash == "" {
			task.ID = naming.IssueID(ref.IssueID)
			task.Status = domain.Status(statusRaw)
			task.Priority = domain.Priority(priorityRaw)
			task.Type = domain.TaskType(typeRaw)
			task.Labels = decodeStringSliceJSON(labelsRaw)
			ref.LastSyncHash = HashTaskForSync(task)
			ref.LastSyncPayload = encodeExternalRefBaselinePayload(task)
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

const (
	externalRefMetadataExternalUpdatedAt = "linearsync.external_updated_at"
	externalRefMetadataLastSyncedAt      = "linearsync.last_synced_at"
	externalRefMetadataLastSyncHash      = "linearsync.last_sync_hash"
	externalRefMetadataLastSyncPayload   = "linearsync.last_sync_payload"
)

func (c *Client) linearSyncExternalRefTarget(ctx context.Context, db *sql.DB, ref ExternalRef) (string, map[string]string, error) {
	row := db.QueryRowContext(ctx, `
		SELECT provider_scope, COALESCE(metadata_json, '')
		FROM issue_external_refs
		WHERE provider = ?
			AND deleted_at IS NULL
			AND (
				issue_id = ?
				OR remote_key = ?
				OR display_key = ?
			)
		ORDER BY
			CASE
				WHEN issue_id = ? AND remote_key = ? THEN 0
				WHEN issue_id = ? THEN 1
				WHEN remote_key = ? THEN 2
				ELSE 3
			END,
			updated_at DESC
		LIMIT 1
	`, ref.Provider, ref.IssueID, ref.ExternalID, ref.ExternalIdentifier, ref.IssueID, ref.ExternalID, ref.IssueID, ref.ExternalID)

	providerScope := ""
	metadataRaw := ""
	err := row.Scan(&providerScope, &metadataRaw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", nil, err
	}
	metadata := map[string]string{}
	if strings.TrimSpace(metadataRaw) != "" {
		if err := json.Unmarshal([]byte(metadataRaw), &metadata); err != nil {
			return "", nil, err
		}
	}
	return providerScope, metadata, nil
}

func applyExternalRefSyncMetadata(metadata map[string]string, ref ExternalRef) {
	if !ref.ExternalUpdatedAt.IsZero() {
		metadata[externalRefMetadataExternalUpdatedAt] = ref.ExternalUpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	if !ref.LastSyncedAt.IsZero() {
		metadata[externalRefMetadataLastSyncedAt] = ref.LastSyncedAt.UTC().Format(time.RFC3339Nano)
	}
	if strings.TrimSpace(ref.LastSyncHash) != "" {
		metadata[externalRefMetadataLastSyncHash] = strings.TrimSpace(ref.LastSyncHash)
	}
	if strings.TrimSpace(ref.LastSyncPayload) != "" {
		metadata[externalRefMetadataLastSyncPayload] = strings.TrimSpace(ref.LastSyncPayload)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func encodeExternalRefBaselinePayload(task domain.Task) string {
	payload := map[string]any{
		"title":       strings.TrimSpace(task.Title),
		"description": strings.TrimSpace(task.Description),
		"priority":    int(task.Priority),
	}
	raw, _ := json.Marshal(payload)
	return string(raw)
}

func (c *Client) GetExternalSyncState(ctx context.Context, provider, projectID string) (ExternalSyncState, bool, error) {
	db, err := c.dbHandle()
	if err != nil {
		return ExternalSyncState{}, false, err
	}
	var state ExternalSyncState
	var lastSuccessRaw, updatedRaw string
	err = db.QueryRowContext(ctx, `
		SELECT provider, project_id, COALESCE(cursor, ''), COALESCE(last_success_at, ''),
			COALESCE(last_error, ''), updated_at
		FROM azedarach_external_sync_state
		WHERE provider = ? AND project_id = ?
	`, strings.TrimSpace(provider), strings.TrimSpace(projectID)).Scan(&state.Provider, &state.ProjectID, &state.Cursor, &lastSuccessRaw, &state.LastError, &updatedRaw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ExternalSyncState{}, false, nil
		}
		return ExternalSyncState{}, false, c.wrapError("external-sync-state-get", projectID, err)
	}
	state.LastSuccessAt = parseTimestamp(lastSuccessRaw)
	state.UpdatedAt = parseTimestamp(updatedRaw)
	return state, true, nil
}

func (c *Client) UpsertExternalSyncState(ctx context.Context, state ExternalSyncState) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	state.Provider = strings.TrimSpace(state.Provider)
	state.ProjectID = strings.TrimSpace(state.ProjectID)
	if state.Provider == "" || state.ProjectID == "" {
		return c.wrapError("external-sync-state-upsert", state.ProjectID, errors.New("provider and project id are required"))
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now().UTC()
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO azedarach_external_sync_state (
			provider, project_id, cursor, last_success_at, last_error, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider, project_id) DO UPDATE SET
			cursor = excluded.cursor,
			last_success_at = excluded.last_success_at,
			last_error = excluded.last_error,
			updated_at = excluded.updated_at
	`, state.Provider, state.ProjectID, nullableString(state.Cursor), formatOptionalTime(state.LastSuccessAt), nullableString(state.LastError), state.UpdatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return c.wrapError("external-sync-state-upsert", state.ProjectID, err)
	}
	return nil
}

func (c *Client) RecordSyncConflict(ctx context.Context, conflict SyncConflict) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	if conflict.ID == "" {
		sum := sha256.Sum256([]byte(strings.Join([]string{
			conflict.Provider, conflict.ProjectID, conflict.IssueID, conflict.Field, conflict.LocalValue, conflict.RemoteValue,
		}, "\x00")))
		conflict.ID = hex.EncodeToString(sum[:12])
	}
	if conflict.DetectedAt.IsZero() {
		conflict.DetectedAt = time.Now().UTC()
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO azedarach_external_sync_conflicts (
			id, provider, project_id, issue_id, field, local_value, remote_value,
			local_updated_at, remote_updated_at, detected_at, resolved_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
		ON CONFLICT(id) DO UPDATE SET
			local_value = excluded.local_value,
			remote_value = excluded.remote_value,
			local_updated_at = excluded.local_updated_at,
			remote_updated_at = excluded.remote_updated_at,
			detected_at = excluded.detected_at,
			resolved_at = NULL
	`, conflict.ID, conflict.Provider, conflict.ProjectID, conflict.IssueID, conflict.Field, conflict.LocalValue, conflict.RemoteValue, formatOptionalTimePtr(conflict.LocalUpdatedAt), formatOptionalTimePtr(conflict.RemoteUpdatedAt), conflict.DetectedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return c.wrapError("sync-conflict-record", conflict.IssueID, err)
	}
	return nil
}

func (c *Client) ListSyncConflicts(ctx context.Context, provider, projectID string, includeResolved bool) ([]SyncConflict, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	query := `
		SELECT id, provider, project_id, issue_id, field, COALESCE(local_value, ''),
			COALESCE(remote_value, ''), COALESCE(local_updated_at, ''),
			COALESCE(remote_updated_at, ''), detected_at, COALESCE(resolved_at, '')
		FROM azedarach_external_sync_conflicts
		WHERE provider = ? AND project_id = ?
	`
	if !includeResolved {
		query += " AND resolved_at IS NULL"
	}
	query += " ORDER BY detected_at DESC"
	rows, err := db.QueryContext(ctx, query, strings.TrimSpace(provider), strings.TrimSpace(projectID))
	if err != nil {
		return nil, c.wrapError("sync-conflict-list", projectID, err)
	}
	defer rows.Close()
	conflicts := []SyncConflict{}
	for rows.Next() {
		var cfl SyncConflict
		var localUpdatedRaw, remoteUpdatedRaw, detectedRaw, resolvedRaw string
		if err := rows.Scan(&cfl.ID, &cfl.Provider, &cfl.ProjectID, &cfl.IssueID, &cfl.Field, &cfl.LocalValue, &cfl.RemoteValue, &localUpdatedRaw, &remoteUpdatedRaw, &detectedRaw, &resolvedRaw); err != nil {
			return nil, err
		}
		cfl.LocalUpdatedAt = parseOptionalTimestamp(localUpdatedRaw)
		cfl.RemoteUpdatedAt = parseOptionalTimestamp(remoteUpdatedRaw)
		cfl.DetectedAt = parseTimestamp(detectedRaw)
		cfl.ResolvedAt = parseOptionalTimestamp(resolvedRaw)
		conflicts = append(conflicts, cfl)
	}
	return conflicts, rows.Err()
}

func HashTaskForSync(task domain.Task) string {
	body := map[string]any{
		"title":       strings.TrimSpace(task.Title),
		"description": strings.TrimSpace(task.Description),
		"status":      string(task.Status),
		"priority":    int(task.Priority),
		"type":        string(task.Type),
		"assignee":    strings.TrimSpace(task.Assignee),
		"labels":      append([]string(nil), task.Labels...),
	}
	raw, _ := json.Marshal(body)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func formatOptionalTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func formatOptionalTimePtr(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}
