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
	_, err = db.ExecContext(ctx, `
		INSERT INTO azedarach_external_issue_refs (
			provider, issue_id, external_id, external_identifier, external_url,
			external_updated_at, last_synced_at, last_sync_hash
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider, issue_id) DO UPDATE SET
			external_id = excluded.external_id,
			external_identifier = excluded.external_identifier,
			external_url = excluded.external_url,
			external_updated_at = excluded.external_updated_at,
			last_synced_at = excluded.last_synced_at,
			last_sync_hash = excluded.last_sync_hash
	`, ref.Provider, ref.IssueID, ref.ExternalID, ref.ExternalIdentifier, nullableString(ref.ExternalURL), formatOptionalTime(ref.ExternalUpdatedAt), ref.LastSyncedAt.UTC().Format(time.RFC3339Nano), ref.LastSyncHash)
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
		SELECT provider, issue_id, external_id, external_identifier,
			COALESCE(external_url, ''), COALESCE(external_updated_at, ''),
			last_synced_at, last_sync_hash
		FROM azedarach_external_issue_refs
		WHERE provider = ?
	`, strings.TrimSpace(provider))
	if err != nil {
		return nil, c.wrapError("external-ref-list", provider, err)
	}
	defer rows.Close()
	refs := []ExternalRef{}
	for rows.Next() {
		var ref ExternalRef
		var externalUpdatedRaw, lastSyncedRaw string
		if err := rows.Scan(&ref.Provider, &ref.IssueID, &ref.ExternalID, &ref.ExternalIdentifier, &ref.ExternalURL, &externalUpdatedRaw, &lastSyncedRaw, &ref.LastSyncHash); err != nil {
			return nil, err
		}
		ref.ExternalUpdatedAt = parseTimestamp(externalUpdatedRaw)
		ref.LastSyncedAt = parseTimestamp(lastSyncedRaw)
		refs = append(refs, ref)
	}
	return refs, rows.Err()
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
