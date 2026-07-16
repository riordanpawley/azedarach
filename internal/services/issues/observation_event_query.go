package issues

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

var ErrInvalidIssueObservationEventQuery = errors.New("invalid issue observation event query")

type IssueObservationEventOrder string

const (
	IssueObservationEventOrderAsc  IssueObservationEventOrder = "asc"
	IssueObservationEventOrderDesc IssueObservationEventOrder = "desc"
)

type IssueObservationEventPayloadFilter struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}

type IssueObservationEventQuery struct {
	Types         []domain.IssueObservationEventType   `json:"event_types,omitempty"`
	Order         IssueObservationEventOrder           `json:"order,omitempty"`
	Limit         int                                  `json:"limit,omitempty"`
	AfterID       int64                                `json:"after_id,omitempty"`
	BeforeID      int64                                `json:"before_id,omitempty"`
	Source        string                               `json:"source,omitempty"`
	SourceCommand string                               `json:"source_command,omitempty"`
	OperationID   string                               `json:"operation_id,omitempty"`
	SessionID     string                               `json:"session_id,omitempty"`
	WorktreePath  string                               `json:"worktree_path,omitempty"`
	ObservedSince *time.Time                           `json:"observed_since,omitempty"`
	ObservedUntil *time.Time                           `json:"observed_until,omitempty"`
	Query         string                               `json:"query,omitempty"`
	PayloadEquals []IssueObservationEventPayloadFilter `json:"payload_equals,omitempty"`
}

type IssueObservationEventPage struct {
	Events       []domain.IssueObservationEvent `json:"events"`
	Order        IssueObservationEventOrder     `json:"order"`
	Limit        int                            `json:"limit"`
	HasMore      bool                           `json:"has_more"`
	FirstID      int64                          `json:"first_id,omitempty"`
	LastID       int64                          `json:"last_id,omitempty"`
	NextAfterID  *int64                         `json:"next_after_id,omitempty"`
	NextBeforeID *int64                         `json:"next_before_id,omitempty"`
}

func normalizeIssueObservationEventQuery(query IssueObservationEventQuery) (IssueObservationEventQuery, error) {
	if query.Order == "" {
		query.Order = IssueObservationEventOrderAsc
	}
	if query.Order != IssueObservationEventOrderAsc && query.Order != IssueObservationEventOrderDesc {
		return IssueObservationEventQuery{}, fmt.Errorf("%w: event order must be asc or desc", ErrInvalidIssueObservationEventQuery)
	}
	if query.AfterID < 0 || query.BeforeID < 0 {
		return IssueObservationEventQuery{}, fmt.Errorf("%w: event id bounds must be non-negative", ErrInvalidIssueObservationEventQuery)
	}
	if query.AfterID > 0 && query.BeforeID > 0 && query.AfterID >= query.BeforeID {
		return IssueObservationEventQuery{}, fmt.Errorf("%w: after_id must be less than before_id", ErrInvalidIssueObservationEventQuery)
	}
	if query.ObservedSince != nil && query.ObservedUntil != nil && query.ObservedSince.After(*query.ObservedUntil) {
		return IssueObservationEventQuery{}, fmt.Errorf("%w: observed_since must not be after observed_until", ErrInvalidIssueObservationEventQuery)
	}
	if query.Limit < 0 {
		return IssueObservationEventQuery{}, fmt.Errorf("%w: event limit must be non-negative", ErrInvalidIssueObservationEventQuery)
	}
	if query.Limit == 0 {
		query.Limit = defaultIssueObservationEventLimit
	}
	if query.Limit > 5000 {
		query.Limit = 5000
	}
	for i := range query.PayloadEquals {
		query.PayloadEquals[i].Key = strings.TrimSpace(query.PayloadEquals[i].Key)
		if _, ok := issueObservationPayloadFilterExpression(query.PayloadEquals[i].Key); !ok {
			return IssueObservationEventQuery{}, fmt.Errorf("%w: invalid payload filter key %q", ErrInvalidIssueObservationEventQuery, query.PayloadEquals[i].Key)
		}
	}
	return query, nil
}

func issueObservationPayloadFilterExpression(key string) (string, bool) {
	switch key {
	case "outcome", "disposition", "decision_id", "revision", "actor_id":
		return `json_extract(events.payload_json, '$.` + key + `')`, true
	default:
		return "", false
	}
}

// QueryIssueObservationEvents applies the daemon-owned event-history contract.
// ID cursors are exclusive, so direction-appropriate page cursors remain stable
// when later event IDs append concurrently.
func (c *Client) QueryIssueObservationEvents(ctx context.Context, issueID string, query IssueObservationEventQuery) (IssueObservationEventPage, error) {
	db, err := c.dbHandle()
	if err != nil {
		return IssueObservationEventPage{}, err
	}
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return IssueObservationEventPage{}, c.wrapError("query-observation-events", "", errors.New("issue id is required"))
	}
	exists, err := c.issueIDExistsIncludingDeleted(ctx, db, issueID)
	if err != nil {
		return IssueObservationEventPage{}, c.wrapError("query-observation-events", issueID, err)
	}
	if !exists {
		return IssueObservationEventPage{}, c.wrapError("query-observation-events", issueID, domain.ErrNotFound)
	}
	query, err = normalizeIssueObservationEventQuery(query)
	if err != nil {
		return IssueObservationEventPage{}, c.wrapError("query-observation-events", issueID, err)
	}

	fromSQL := "issue_observation_events AS events"
	where := []string{"events.issue_id = ?"}
	args := []any{issueID}
	if expression := domain.ContentQueryFTSExpression(query.Query); expression != "" {
		fromSQL += " JOIN issue_observation_event_search_fts ON issue_observation_event_search_fts.rowid = events.id"
		where = append(where, "issue_observation_event_search_fts MATCH ?")
		args = append(args, expression)
	}
	if len(query.Types) > 0 {
		values := make([]string, 0, len(query.Types))
		for _, eventType := range query.Types {
			if value := strings.TrimSpace(string(eventType)); value != "" {
				values = append(values, value)
			}
		}
		if len(values) > 0 {
			where = append(where, "events.event_type IN ("+strings.TrimSuffix(strings.Repeat("?,", len(values)), ",")+")")
			for _, value := range values {
				args = append(args, value)
			}
		}
	}
	for _, filter := range []struct {
		column string
		value  string
	}{
		{"source", query.Source},
		{"source_command", query.SourceCommand},
		{"operation_id", query.OperationID},
		{"session_id", query.SessionID},
		{"worktree_path", query.WorktreePath},
	} {
		if value := strings.TrimSpace(filter.value); value != "" {
			where = append(where, "events."+filter.column+" = ?")
			args = append(args, value)
		}
	}
	if query.AfterID > 0 {
		where = append(where, "events.id > ?")
		args = append(args, query.AfterID)
	}
	if query.BeforeID > 0 {
		where = append(where, "events.id < ?")
		args = append(args, query.BeforeID)
	}
	if query.ObservedSince != nil {
		where = append(where, "julianday(events.observed_at) >= julianday(?)")
		args = append(args, query.ObservedSince.UTC().Format(time.RFC3339Nano))
	}
	if query.ObservedUntil != nil {
		where = append(where, "julianday(events.observed_at) <= julianday(?)")
		args = append(args, query.ObservedUntil.UTC().Format(time.RFC3339Nano))
	}
	payloadFilters := append([]IssueObservationEventPayloadFilter(nil), query.PayloadEquals...)
	sort.Slice(payloadFilters, func(i, j int) bool { return payloadFilters[i].Key < payloadFilters[j].Key })
	for _, filter := range payloadFilters {
		expression, _ := issueObservationPayloadFilterExpression(filter.Key)
		where = append(where, expression+" IS ?")
		args = append(args, filter.Value)
	}
	order := "ASC"
	if query.Order == IssueObservationEventOrderDesc {
		order = "DESC"
	}
	candidates := make([]domain.IssueObservationEvent, 0, query.Limit+1)
	scanLimit := max(query.Limit+1, 128)
	scanCursor := int64(0)
	for len(candidates) <= query.Limit {
		iterationWhere := append([]string(nil), where...)
		iterationArgs := append([]any(nil), args...)
		if scanCursor > 0 {
			if query.Order == IssueObservationEventOrderDesc {
				iterationWhere = append(iterationWhere, "events.id < ?")
			} else {
				iterationWhere = append(iterationWhere, "events.id > ?")
			}
			iterationArgs = append(iterationArgs, scanCursor)
		}
		iterationArgs = append(iterationArgs, scanLimit)
		rows, queryErr := db.QueryContext(ctx, `
			SELECT events.id, events.issue_id, events.event_type, events.observed_at, events.source, events.source_command, events.operation_id, events.session_id, events.worktree_path, events.payload_json
			FROM `+fromSQL+`
			WHERE `+strings.Join(iterationWhere, " AND ")+`
			ORDER BY events.id `+order+`
			LIMIT ?
		`, iterationArgs...)
		if queryErr != nil {
			return IssueObservationEventPage{}, c.wrapError("query-observation-events", issueID, queryErr)
		}
		scanned := 0
		for rows.Next() {
			event, scanErr := scanIssueObservationEvent(rows)
			if scanErr != nil {
				rows.Close()
				return IssueObservationEventPage{}, c.wrapError("query-observation-events", issueID, scanErr)
			}
			scanned++
			scanCursor = event.ID
			if domain.IssueObservationEventMatchesQuery(event, query.Query) {
				candidates = append(candidates, event)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return IssueObservationEventPage{}, c.wrapError("query-observation-events", issueID, err)
		}
		if err := rows.Close(); err != nil {
			return IssueObservationEventPage{}, c.wrapError("query-observation-events", issueID, err)
		}
		if scanned < scanLimit {
			break
		}
	}

	page := IssueObservationEventPage{Order: query.Order, Limit: query.Limit, Events: candidates}
	if len(page.Events) > query.Limit {
		page.HasMore = true
		page.Events = page.Events[:query.Limit]
	}
	if len(page.Events) > 0 {
		page.FirstID = page.Events[0].ID
		page.LastID = page.Events[len(page.Events)-1].ID
		if page.HasMore {
			cursor := page.LastID
			if query.Order == IssueObservationEventOrderDesc {
				page.NextBeforeID = &cursor
			} else {
				page.NextAfterID = &cursor
			}
		}
	}
	return page, nil
}
