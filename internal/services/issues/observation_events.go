package issues

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/sqliteutil"
)

const defaultIssueObservationEventLimit = 500

type IssueObservationEventParams struct {
	Type          domain.IssueObservationEventType
	ObservedAt    time.Time
	Source        string
	SourceCommand string
	OperationID   string
	SessionID     string
	WorktreePath  string
	Payload       map[string]any
}

type IssueObservationEventListOptions struct {
	Types       []domain.IssueObservationEventType
	Limit       int
	NewestFirst bool
}

func (c *Client) AppendIssueObservationEvent(ctx context.Context, issueID string, params IssueObservationEventParams) (domain.IssueObservationEvent, error) {
	var event domain.IssueObservationEvent
	err := retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(ctx context.Context) error {
			return sqliteutil.WithWriteLock(c.dbPath, func() error {
				db, err := c.dbHandle()
				if err != nil {
					return err
				}
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					return c.wrapError("append-observation-event", issueID, err)
				}
				defer func() {
					if tx != nil {
						_ = tx.Rollback()
					}
				}()
				if err := c.requireIssueExists(ctx, tx, issueID, "append-observation-event"); err != nil {
					return err
				}
				id, err := c.insertIssueObservationEvent(ctx, tx, issueID, params)
				if err != nil {
					return c.wrapError("append-observation-event", issueID, err)
				}
				if err := tx.Commit(); err != nil {
					return c.wrapError("append-observation-event", issueID, err)
				}
				tx = nil
				event, err = c.getIssueObservationEventByID(ctx, id)
				if err != nil {
					return c.wrapError("append-observation-event", issueID, err)
				}
				return nil
			})
		})
	})
	return event, err
}

func (c *Client) ListIssueObservationEvents(ctx context.Context, issueID string, opts IssueObservationEventListOptions) ([]domain.IssueObservationEvent, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return nil, c.wrapError("list-observation-events", "", errors.New("issue id is required"))
	}
	exists, err := c.issueIDExistsIncludingDeleted(ctx, db, issueID)
	if err != nil {
		return nil, c.wrapError("list-observation-events", issueID, err)
	}
	if !exists {
		return nil, c.wrapError("list-observation-events", issueID, domain.ErrNotFound)
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultIssueObservationEventLimit
	}
	if limit > 5000 {
		limit = 5000
	}
	args := []any{issueID}
	typeFilters := make([]string, 0, len(opts.Types))
	for _, eventType := range opts.Types {
		trimmed := strings.TrimSpace(string(eventType))
		if trimmed == "" {
			continue
		}
		typeFilters = append(typeFilters, trimmed)
		args = append(args, trimmed)
	}
	filterSQL := ""
	if len(typeFilters) > 0 {
		filterSQL = " AND event_type IN (" + strings.TrimSuffix(strings.Repeat("?,", len(typeFilters)), ",") + ")"
	}
	args = append(args, limit)
	orderBy := "id ASC"
	if opts.NewestFirst {
		orderBy = "observed_at DESC, id DESC"
	}
	rows, err := db.QueryContext(ctx, `
        SELECT id, issue_id, event_type, observed_at, source, source_command, operation_id, session_id, worktree_path, payload_json
        FROM issue_observation_events
        WHERE issue_id = ?`+filterSQL+`
        ORDER BY `+orderBy+`
        LIMIT ?
    `, args...)
	if err != nil {
		return nil, c.wrapError("list-observation-events", issueID, err)
	}
	defer rows.Close()
	events := make([]domain.IssueObservationEvent, 0, 16)
	for rows.Next() {
		event, err := scanIssueObservationEvent(rows)
		if err != nil {
			return nil, c.wrapError("list-observation-events", issueID, err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("list-observation-events", issueID, err)
	}
	return events, nil
}

func (c *Client) appendIssueObservationEvent(ctx context.Context, execer sqlIssueExecer, issueID string, eventType domain.IssueObservationEventType, payload map[string]any) error {
	_, err := c.insertIssueObservationEvent(ctx, execer, issueID, IssueObservationEventParams{
		Type:    eventType,
		Source:  "issue-store",
		Payload: payload,
	})
	return err
}

func (c *Client) insertIssueObservationEvent(ctx context.Context, execer sqlIssueExecer, issueID string, params IssueObservationEventParams) (int64, error) {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return 0, errors.New("issue id is required")
	}
	eventType := strings.TrimSpace(string(params.Type))
	if eventType == "" {
		return 0, errors.New("event type is required")
	}
	observedAt := params.ObservedAt
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	payloadJSON, err := marshalObservationPayload(params.Payload)
	if err != nil {
		return 0, err
	}
	result, err := execer.ExecContext(ctx, `
		INSERT INTO issue_observation_events (
			issue_id,
			event_type,
			observed_at,
			source,
			source_command,
			operation_id,
			session_id,
			worktree_path,
			payload_json
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, issueID, eventType, observedAt.UTC().Format(time.RFC3339Nano), strings.TrimSpace(params.Source), strings.TrimSpace(params.SourceCommand), strings.TrimSpace(params.OperationID), strings.TrimSpace(params.SessionID), strings.TrimSpace(params.WorktreePath), payloadJSON)
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read observation event id: %w", err)
	}
	return id, nil
}

func (c *Client) getIssueObservationEventByID(ctx context.Context, id int64) (domain.IssueObservationEvent, error) {
	db, err := c.dbHandle()
	if err != nil {
		return domain.IssueObservationEvent{}, err
	}
	return scanIssueObservationEvent(db.QueryRowContext(ctx, `
		SELECT id, issue_id, event_type, observed_at, source, source_command, operation_id, session_id, worktree_path, payload_json
		FROM issue_observation_events
		WHERE id = ?
	`, id))
}

func (c *Client) requireIssueExists(ctx context.Context, queryer sqlIssueQueryer, issueID, operation string) error {
	exists, err := c.issueExists(ctx, queryer, strings.TrimSpace(issueID))
	if err != nil {
		return c.wrapError(operation, issueID, err)
	}
	if !exists {
		return c.wrapError(operation, issueID, domain.ErrNotFound)
	}
	return nil
}

func (c *Client) issueIDExistsIncludingDeleted(ctx context.Context, queryer sqlIssueQueryer, issueID string) (bool, error) {
	var exists bool
	if err := queryer.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM issues
			WHERE id = ?
			UNION ALL
			SELECT 1
			FROM issue_observation_events
			WHERE issue_id = ?
		)
	`, issueID, issueID).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

type issueObservationEventScanner interface {
	Scan(...any) error
}

func scanIssueObservationEvent(scanner issueObservationEventScanner) (domain.IssueObservationEvent, error) {
	var event domain.IssueObservationEvent
	var issueID string
	var eventType string
	var observedRaw string
	var payloadRaw string
	if err := scanner.Scan(
		&event.ID,
		&issueID,
		&eventType,
		&observedRaw,
		&event.Source,
		&event.SourceCommand,
		&event.OperationID,
		&event.SessionID,
		&event.WorktreePath,
		&payloadRaw,
	); err != nil {
		return domain.IssueObservationEvent{}, err
	}
	parsedIssueID, err := naming.ParseIssueID(issueID)
	if err != nil {
		return domain.IssueObservationEvent{}, fmt.Errorf("parse issue id %q: %w", issueID, err)
	}
	event.IssueID = parsedIssueID
	event.Type = domain.IssueObservationEventType(eventType)
	event.ObservedAt = parseTimestamp(observedRaw)
	payload := map[string]any{}
	if strings.TrimSpace(payloadRaw) != "" {
		if err := json.Unmarshal([]byte(payloadRaw), &payload); err != nil {
			return domain.IssueObservationEvent{}, fmt.Errorf("decode payload: %w", err)
		}
	}
	event.Payload = payload
	return event, nil
}

func marshalObservationPayload(payload map[string]any) (string, error) {
	if payload == nil {
		return "{}", nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal observation payload: %w", err)
	}
	if len(data) == 0 || string(data) == "null" {
		return "{}", nil
	}
	return string(data), nil
}
