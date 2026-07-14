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
	Types         []domain.IssueObservationEventType
	Limit         int
	NewestFirst   bool
	NewestIDFirst bool
}

type LatestIssueObservationEventOptions struct {
	IssueIDs                []string
	Type                    domain.IssueObservationEventType
	Source                  string
	SourceCommands          []string
	CommandOutcomePairs     []IssueObservationCommandOutcomePair
	RequiredPayloadTextKeys []string
	CurrentReviewEpoch      bool
	InvalidatedByStatuses   []domain.Status
}

type IssueObservationCommandOutcomePair struct {
	SourceCommand string
	Outcomes      []string
}

// ListProjectIssueObservationEvents returns the durable project event stream
// after a cursor. Each issues.Client is already scoped to one project database,
// so the global event id is a stable project watch cursor across daemon restarts.
func (c *Client) ListProjectIssueObservationEvents(ctx context.Context, afterID int64, limit int) ([]domain.IssueObservationEvent, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	if afterID < 0 {
		afterID = 0
	}
	if limit <= 0 {
		limit = defaultIssueObservationEventLimit
	}
	if limit > 5000 {
		limit = 5000
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, issue_id, event_type, observed_at, source, source_command, operation_id, session_id, worktree_path, payload_json
		FROM issue_observation_events
		WHERE id > ?
		ORDER BY id ASC
		LIMIT ?
	`, afterID, limit)
	if err != nil {
		return nil, c.wrapError("list-project-observation-events", "", err)
	}
	defer rows.Close()
	events := make([]domain.IssueObservationEvent, 0, min(limit, 64))
	for rows.Next() {
		event, scanErr := scanIssueObservationEvent(rows)
		if scanErr != nil {
			return nil, c.wrapError("list-project-observation-events", "", scanErr)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("list-project-observation-events", "", err)
	}
	return events, nil
}

func (c *Client) AppendIssueObservationEvent(ctx context.Context, issueID string, params IssueObservationEventParams) (domain.IssueObservationEvent, error) {
	var eventID int64
	err := c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(ctx context.Context) error {
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
			eventID = id
			return nil
		})
	})
	if err != nil {
		return domain.IssueObservationEvent{}, err
	}
	event, err := c.getIssueObservationEventByID(ctx, eventID)
	if err != nil {
		return domain.IssueObservationEvent{}, c.wrapError("append-observation-event", issueID, err)
	}
	return event, nil
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
	if opts.NewestIDFirst {
		orderBy = "id DESC"
	} else if opts.NewestFirst {
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

// ListIssueReviewReadyObservationEvents returns the complete typed event set
// used to reduce review-ready publications and acceptance evidence. It is
// intentionally uncapped: callers require one authoritative decision across
// the issue's full durable history.
func (c *Client) ListIssueReviewReadyObservationEvents(ctx context.Context, issueID string) ([]domain.IssueObservationEvent, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return nil, c.wrapError("list-review-ready-observation-events", "", errors.New("issue id is required"))
	}
	exists, err := c.issueIDExistsIncludingDeleted(ctx, db, issueID)
	if err != nil {
		return nil, c.wrapError("list-review-ready-observation-events", issueID, err)
	}
	if !exists {
		return nil, c.wrapError("list-review-ready-observation-events", issueID, domain.ErrNotFound)
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, issue_id, event_type, observed_at, source, source_command, operation_id, session_id, worktree_path, payload_json
		FROM issue_observation_events
		WHERE issue_id = ?
		  AND (
			event_type = ?
			OR LOWER(REPLACE(REPLACE(TRIM(event_type), '_', '.'), '-', '.')) IN (?, ?, ?, ?)
		  )
		ORDER BY id ASC
	`, issueID,
		string(domain.IssueEventIssueStatusChanged),
		string(domain.IssueEventEvidenceSubmitted),
		"worker.integration.ready",
		"worker.ready",
		"worker.complete",
	)
	if err != nil {
		return nil, c.wrapError("list-review-ready-observation-events", issueID, err)
	}
	defer rows.Close()
	events := make([]domain.IssueObservationEvent, 0, 16)
	for rows.Next() {
		event, scanErr := scanIssueObservationEvent(rows)
		if scanErr != nil {
			return nil, c.wrapError("list-review-ready-observation-events", issueID, scanErr)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("list-review-ready-observation-events", issueID, err)
	}
	return events, nil
}

// ListIssueDecisionObservationEvents returns the complete replay authority for
// material decision changes and exact-revision acknowledgements on one issue.
func (c *Client) ListIssueDecisionObservationEvents(ctx context.Context, issueID string) ([]domain.IssueObservationEvent, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return nil, c.wrapError("list-decision-observation-events", "", errors.New("issue id is required"))
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, issue_id, event_type, observed_at, source, source_command, operation_id, session_id, worktree_path, payload_json
		FROM issue_observation_events
		WHERE issue_id = ? AND event_type IN (?, ?)
		ORDER BY observed_at ASC, id ASC
	`, issueID, string(domain.IssueEventDecisionChanged), string(domain.IssueEventDecisionAcknowledged))
	if err != nil {
		return nil, c.wrapError("list-decision-observation-events", issueID, err)
	}
	defer rows.Close()
	events := make([]domain.IssueObservationEvent, 0, 8)
	for rows.Next() {
		event, scanErr := scanIssueObservationEvent(rows)
		if scanErr != nil {
			return nil, c.wrapError("list-decision-observation-events", issueID, scanErr)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("list-decision-observation-events", issueID, err)
	}
	return events, nil
}

// ListIssueDecisionObservationEventsByIssue batches orchestration snapshot
// replay for many issues so decision visibility does not add one query per
// candidate on large boards.
func (c *Client) ListIssueDecisionObservationEventsByIssue(ctx context.Context, issueIDs []string) (map[string][]domain.IssueObservationEvent, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	issueIDs = normalizeOrderedIDs(issueIDs)
	out := make(map[string][]domain.IssueObservationEvent, len(issueIDs))
	if len(issueIDs) == 0 {
		return out, nil
	}
	encoded, err := json.Marshal(issueIDs)
	if err != nil {
		return nil, c.wrapError("list-decision-observation-events-batch", "", err)
	}
	rows, err := db.QueryContext(ctx, `
		WITH requested(issue_id) AS (SELECT value FROM json_each(?))
		SELECT e.id, e.issue_id, e.event_type, e.observed_at, e.source, e.source_command, e.operation_id, e.session_id, e.worktree_path, e.payload_json
		FROM issue_observation_events e
		JOIN requested r ON r.issue_id = e.issue_id
		WHERE e.event_type IN (?, ?)
		ORDER BY e.issue_id, e.id ASC
	`, string(encoded), string(domain.IssueEventDecisionChanged), string(domain.IssueEventDecisionAcknowledged))
	if err != nil {
		return nil, c.wrapError("list-decision-observation-events-batch", "", err)
	}
	defer rows.Close()
	for rows.Next() {
		event, scanErr := scanIssueObservationEvent(rows)
		if scanErr != nil {
			return nil, c.wrapError("list-decision-observation-events-batch", "", scanErr)
		}
		issueID := event.IssueID.String()
		out[issueID] = append(out[issueID], event)
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("list-decision-observation-events-batch", "", err)
	}
	return out, nil
}

// ListLatestIssueObservationEventsByIssue returns at most one matching event
// per issue in one SQLite query. Callers retain authority for interpreting the
// candidate; filters only keep the persistent read bounded and indexable.
func (c *Client) ListLatestIssueObservationEventsByIssue(ctx context.Context, opts LatestIssueObservationEventOptions) (map[string]domain.IssueObservationEvent, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	eventType := strings.TrimSpace(string(opts.Type))
	if eventType == "" {
		return nil, c.wrapError("list-latest-observation-events-by-issue", "", errors.New("event type is required"))
	}
	issueIDs := make([]string, 0, len(opts.IssueIDs))
	seenIssueIDs := make(map[string]struct{}, len(opts.IssueIDs))
	for _, issueID := range opts.IssueIDs {
		issueID = strings.TrimSpace(issueID)
		if issueID == "" {
			continue
		}
		if _, exists := seenIssueIDs[issueID]; exists {
			continue
		}
		seenIssueIDs[issueID] = struct{}{}
		issueIDs = append(issueIDs, issueID)
	}
	if len(issueIDs) == 0 {
		return map[string]domain.IssueObservationEvent{}, nil
	}
	issueIDsJSON, err := json.Marshal(issueIDs)
	if err != nil {
		return nil, c.wrapError("list-latest-observation-events-by-issue", "", err)
	}
	clauses := []string{"event_type = ?"}
	args := []any{string(issueIDsJSON), eventType}
	if source := strings.TrimSpace(opts.Source); source != "" {
		clauses = append(clauses, "TRIM(source) = ?")
		args = append(args, source)
	}
	commands := make([]string, 0, len(opts.SourceCommands))
	for _, command := range opts.SourceCommands {
		if command = strings.TrimSpace(command); command != "" {
			commands = append(commands, command)
		}
	}
	if len(commands) > 0 {
		clauses = append(clauses, "TRIM(source_command) IN ("+strings.TrimSuffix(strings.Repeat("?,", len(commands)), ",")+")")
		for _, command := range commands {
			args = append(args, command)
		}
	}
	pairClauses := make([]string, 0, len(opts.CommandOutcomePairs))
	for _, pair := range opts.CommandOutcomePairs {
		command := strings.TrimSpace(pair.SourceCommand)
		outcomes := make([]string, 0, len(pair.Outcomes))
		for _, outcome := range pair.Outcomes {
			if outcome = strings.TrimSpace(outcome); outcome != "" {
				outcomes = append(outcomes, outcome)
			}
		}
		if command == "" || len(outcomes) == 0 {
			continue
		}
		pairClauses = append(pairClauses, "(TRIM(events.source_command) = ? AND TRIM(CAST(json_extract(events.payload_json, '$.outcome') AS TEXT)) IN ("+strings.TrimSuffix(strings.Repeat("?,", len(outcomes)), ",")+"))")
		args = append(args, command)
		for _, outcome := range outcomes {
			args = append(args, outcome)
		}
	}
	if len(pairClauses) > 0 {
		clauses = append(clauses, "("+strings.Join(pairClauses, " OR ")+")")
	}
	for _, key := range opts.RequiredPayloadTextKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		path := `$."` + strings.ReplaceAll(key, `"`, `\"`) + `"`
		clauses = append(clauses, "json_type(payload_json, ?) = 'text' AND NULLIF(TRIM(CAST(json_extract(payload_json, ?) AS TEXT)), '') IS NOT NULL")
		args = append(args, path, path)
	}
	epochStatuses := make([]string, 0, len(opts.InvalidatedByStatuses)+1)
	seenEpochStatuses := make(map[string]struct{}, len(opts.InvalidatedByStatuses)+1)
	addEpochStatus := func(status domain.Status) {
		value := strings.ToLower(strings.TrimSpace(string(status)))
		if value == "" {
			return
		}
		if _, exists := seenEpochStatuses[value]; exists {
			return
		}
		seenEpochStatuses[value] = struct{}{}
		epochStatuses = append(epochStatuses, value)
	}
	if opts.CurrentReviewEpoch {
		addEpochStatus(domain.StatusInReview)
	}
	for _, status := range opts.InvalidatedByStatuses {
		addEpochStatus(status)
	}
	if len(epochStatuses) > 0 {
		clauses = append(clauses, `NOT EXISTS (
			SELECT 1
			FROM issue_observation_events AS epoch
			WHERE epoch.issue_id = events.issue_id
			  AND epoch.id > events.id
			  AND epoch.event_type = ?
			  AND TRIM(epoch.source) = 'issue-store'
			  AND LOWER(TRIM(CAST(json_extract(epoch.payload_json, '$.to_status') AS TEXT))) IN (`+strings.TrimSuffix(strings.Repeat("?,", len(epochStatuses)), ",")+`)
		)`)
		args = append(args, string(domain.IssueEventIssueStatusChanged))
		for _, status := range epochStatuses {
			args = append(args, status)
		}
	}
	rows, err := db.QueryContext(ctx, `
		WITH candidate_issues(issue_id) AS (
			SELECT DISTINCT TRIM(CAST(value AS TEXT))
			FROM json_each(?)
			WHERE type = 'text' AND TRIM(CAST(value AS TEXT)) <> ''
		), ranked AS (
			SELECT id, issue_id, event_type, observed_at, source, source_command, operation_id, session_id, worktree_path, payload_json,
				ROW_NUMBER() OVER (PARTITION BY issue_id ORDER BY id DESC) AS event_rank
			FROM issue_observation_events AS events
			JOIN candidate_issues USING (issue_id)
			WHERE `+strings.Join(clauses, " AND ")+`
		)
		SELECT id, issue_id, event_type, observed_at, source, source_command, operation_id, session_id, worktree_path, payload_json
		FROM ranked
		WHERE event_rank = 1
		ORDER BY issue_id ASC
	`, args...)
	if err != nil {
		return nil, c.wrapError("list-latest-observation-events-by-issue", "", err)
	}
	defer rows.Close()
	events := make(map[string]domain.IssueObservationEvent)
	for rows.Next() {
		event, scanErr := scanIssueObservationEvent(rows)
		if scanErr != nil {
			return nil, c.wrapError("list-latest-observation-events-by-issue", "", scanErr)
		}
		events[event.IssueID.String()] = event
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("list-latest-observation-events-by-issue", "", err)
	}
	return events, nil
}

// InvestigationAcceptances reads the durable evidence needed to decide who
// has authority over each investigation's findings. The store selects the
// evidence; the domain owns its meaning.
func (c *Client) InvestigationAcceptances(ctx context.Context, tasks []domain.Task) (map[string]domain.InvestigationAcceptance, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(tasks))
	tasksByID := make(map[string]domain.Task)
	for _, task := range tasks {
		if task.Type != domain.TypeInvestigation {
			continue
		}
		id := strings.TrimSpace(task.ID.String())
		if id == "" {
			continue
		}
		ids = append(ids, id)
		tasksByID[id] = task
	}
	if len(ids) == 0 {
		return map[string]domain.InvestigationAcceptance{}, nil
	}
	args := make([]any, 0, len(ids)+4)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args,
		string(domain.IssueEventInvestigationDisposition),
		string(domain.IssueEventReviewCompleted),
		string(domain.IssueEventHumanInputProvided),
		string(domain.IssueEventIssueStatusChanged),
	)
	rows, err := db.QueryContext(ctx, `
		SELECT id, issue_id, event_type, observed_at, source, source_command, operation_id, session_id, worktree_path, payload_json
		FROM issue_observation_events
		WHERE issue_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)
		  AND event_type IN (?,?,?,?)
		ORDER BY id ASC
	`, args...)
	if err != nil {
		return nil, c.wrapError("list-investigation-acceptances", "", err)
	}
	defer rows.Close()
	eventsByID := make(map[string][]domain.IssueObservationEvent, len(ids))
	for rows.Next() {
		event, scanErr := scanIssueObservationEvent(rows)
		if scanErr != nil {
			return nil, c.wrapError("list-investigation-acceptances", event.IssueID.String(), scanErr)
		}
		id := event.IssueID.String()
		eventsByID[id] = append(eventsByID[id], event)
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("list-investigation-acceptances", "", err)
	}
	out := make(map[string]domain.InvestigationAcceptance, len(ids))
	for _, id := range ids {
		out[id] = domain.EvaluateInvestigationAcceptance(tasksByID[id], eventsByID[id])
	}
	return out, nil
}

func (c *Client) appendIssueObservationEvent(ctx context.Context, execer sqlIssueDBTX, issueID string, eventType domain.IssueObservationEventType, payload map[string]any) error {
	_, err := c.insertIssueObservationEvent(ctx, execer, issueID, IssueObservationEventParams{
		Type:    eventType,
		Source:  "issue-store",
		Payload: payload,
	})
	return err
}

func (c *Client) insertIssueObservationEvent(ctx context.Context, execer sqlIssueDBTX, issueID string, params IssueObservationEventParams) (int64, error) {
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
	operation := domain.ProjectionDeltaUpsert
	if params.Type == domain.IssueEventIssueDeleted {
		operation = domain.ProjectionDeltaDelete
	}
	if !issueEventChangesIssueProjection(params.Type) {
		if _, err := appendProjectionEmptyAdvance(ctx, execer, ProjectionSourceAdvance{
			ProjectID: "default", SourceAuthority: "legacy_issue_observation", SourcePosition: fmt.Sprint(id),
			IdempotencyKey: fmt.Sprintf("issue-observation:%d", id), CommittedAt: observedAt,
		}); err != nil {
			return 0, fmt.Errorf("append issue observation empty projection advance: %w", err)
		}
		return id, nil
	}
	deltaPayload, err := c.issueProjectionDeltaPayload(ctx, execer, issueID, operation)
	if err != nil {
		return 0, fmt.Errorf("build issue projection delta: %w", err)
	}
	if _, err := appendProjectionDelta(ctx, execer, ProjectionDeltaParams{
		ProjectID:      "default",
		Kind:           domain.ProjectionKindIssue,
		Key:            issueID,
		Operation:      operation,
		IdempotencyKey: fmt.Sprintf("issue-observation:%d", id),
		Payload:        deltaPayload,
		CommittedAt:    observedAt,
	}); err != nil {
		return 0, fmt.Errorf("append issue observation projection delta: %w", err)
	}
	return id, nil
}

func issueEventChangesIssueProjection(eventType domain.IssueObservationEventType) bool {
	switch eventType {
	case domain.IssueEventIssueCreated, domain.IssueEventIssueStatusChanged, domain.IssueEventIssueDetailsChanged,
		domain.IssueEventIssueNotesAppended, domain.IssueEventIssueDependencyAdded, domain.IssueEventIssueDependencyRemoved,
		domain.IssueEventIssueArchived, domain.IssueEventIssueUnarchived, domain.IssueEventIssueDeleted:
		return true
	default:
		return false
	}
}

func (c *Client) issueProjectionDeltaPayload(ctx context.Context, db sqlIssueDBTX, issueID string, operation domain.ProjectionDeltaOperation) ([]byte, error) {
	payload := domain.IssueProjectionDeltaPayload{
		SchemaVersion: domain.IssueProjectionDeltaSchemaVersion,
		IssueID:       strings.TrimSpace(issueID),
		Deleted:       operation == domain.ProjectionDeltaDelete,
	}
	if operation != domain.ProjectionDeltaDelete {
		tasks, err := c.queryTasks(ctx, db, `
			SELECT id,title,COALESCE(description,''),COALESCE(notes,''),COALESCE(design,''),COALESCE(acceptance,''),
				COALESCE(assignee,''),COALESCE(labels_json,'[]'),estimate,status,COALESCE(disposition,''),
				COALESCE(engagement,''),COALESCE(visibility,''),archived_at,priority,issue_type,
				COALESCE(implementations_json,'[]'),created_at,updated_at
			FROM issues WHERE id=?
		`, issueID)
		if err != nil {
			return nil, err
		}
		if len(tasks) != 1 {
			return nil, fmt.Errorf("canonical issue projection %s: %w", issueID, domain.ErrNotFound)
		}
		canonical := domain.CanonicalIssueProjectionTask(tasks[0])
		payload.Issue = &canonical
	}
	return json.Marshal(payload)
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
