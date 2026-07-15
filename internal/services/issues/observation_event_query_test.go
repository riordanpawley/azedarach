package issues

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQueryIssueObservationEventsStableDescendingPaginationWithConcurrentAppend(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	issueID, err := client.Create(ctx, CreateTaskParams{Title: "Page events", Type: domain.TypeTask, Priority: domain.P2})
	require.NoError(t, err)

	ids := make([]int64, 0, 5)
	for i := 0; i < 5; i++ {
		event, appendErr := client.AppendIssueObservationEvent(ctx, issueID, IssueObservationEventParams{
			Type: domain.IssueEventProgressRecorded, Source: "worker", Payload: map[string]any{"summary": fmt.Sprintf("projection checkpoint %d", i)},
		})
		require.NoError(t, appendErr)
		ids = append(ids, event.ID)
	}

	first, err := client.QueryIssueObservationEvents(ctx, issueID, IssueObservationEventQuery{Order: IssueObservationEventOrderDesc, Limit: 2})
	require.NoError(t, err)
	require.Len(t, first.Events, 2)
	assert.Equal(t, []int64{ids[4], ids[3]}, []int64{first.Events[0].ID, first.Events[1].ID})
	require.True(t, first.HasMore)
	require.NotNil(t, first.NextBeforeID)
	assert.Equal(t, ids[3], *first.NextBeforeID)

	appended, err := client.AppendIssueObservationEvent(ctx, issueID, IssueObservationEventParams{Type: domain.IssueEventProgressRecorded, Source: "worker", Payload: map[string]any{"summary": "concurrent append"}})
	require.NoError(t, err)
	second, err := client.QueryIssueObservationEvents(ctx, issueID, IssueObservationEventQuery{Order: IssueObservationEventOrderDesc, BeforeID: *first.NextBeforeID, Limit: 2})
	require.NoError(t, err)
	require.Len(t, second.Events, 2)
	assert.Equal(t, []int64{ids[2], ids[1]}, []int64{second.Events[0].ID, second.Events[1].ID})
	for _, event := range second.Events {
		assert.NotEqual(t, appended.ID, event.ID)
		assert.NotEqual(t, first.Events[0].ID, event.ID)
		assert.NotEqual(t, first.Events[1].ID, event.ID)
	}
}

func TestQueryIssueObservationEventsCombinesIndexedTextMetadataRangeAndPayloadFilters(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	issueID, err := client.Create(ctx, CreateTaskParams{Title: "Filter events", Type: domain.TypeTask, Priority: domain.P2})
	require.NoError(t, err)
	base := time.Date(2026, 7, 15, 1, 0, 0, 0, time.UTC)
	before, err := client.AppendIssueObservationEvent(ctx, issueID, IssueObservationEventParams{Type: domain.IssueEventProgressRecorded, ObservedAt: base, Source: "worker", Payload: map[string]any{"summary": "projection checkpoint", "outcome": "ignored"}})
	require.NoError(t, err)
	want, err := client.AppendIssueObservationEvent(ctx, issueID, IssueObservationEventParams{Type: domain.IssueEventValidationPassed, ObservedAt: base.Add(time.Minute), Source: "worker", SourceCommand: "gate", OperationID: "op-1", SessionID: "session-1", WorktreePath: "/tmp/worktree", Payload: map[string]any{"body": "Projection checkpoint reached", "outcome": "accepted", "revision": 2}})
	require.NoError(t, err)
	after, err := client.AppendIssueObservationEvent(ctx, issueID, IssueObservationEventParams{Type: domain.IssueEventValidationPassed, ObservedAt: base.Add(2 * time.Minute), Source: "other", Payload: map[string]any{"summary": "projection checkpoint", "outcome": "accepted"}})
	require.NoError(t, err)

	page, err := client.QueryIssueObservationEvents(ctx, issueID, IssueObservationEventQuery{
		Types: []domain.IssueObservationEventType{domain.IssueEventValidationPassed}, Order: IssueObservationEventOrderAsc, Limit: 5,
		AfterID: before.ID, BeforeID: after.ID + 1, Source: "worker", SourceCommand: "gate", OperationID: "op-1", SessionID: "session-1", WorktreePath: "/tmp/worktree",
		ObservedSince: ptrTime(base), ObservedUntil: ptrTime(base.Add(2 * time.Minute)), Query: "projection checkpoint",
		PayloadEquals: []IssueObservationEventPayloadFilter{{Key: "outcome", Value: "accepted"}, {Key: "revision", Value: 2}},
	})
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	assert.Equal(t, want.ID, page.Events[0].ID)
	assert.False(t, page.HasMore)
}

func TestQueryIssueObservationEventsFTSAndMetadataPlansUseIndexes(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	db, err := client.dbHandle()
	require.NoError(t, err)

	assertPlanContains := func(query string, args []any, fragment string) {
		t.Helper()
		rows, queryErr := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, args...)
		require.NoError(t, queryErr)
		defer rows.Close()
		var details []string
		for rows.Next() {
			var id, parent, notUsed int
			var detail string
			require.NoError(t, rows.Scan(&id, &parent, &notUsed, &detail))
			details = append(details, detail)
		}
		require.NoError(t, rows.Err())
		assert.Contains(t, strings.Join(details, "\n"), fragment)
	}

	assertPlanContains(`SELECT events.id FROM issue_observation_event_search_fts JOIN issue_observation_events AS events ON events.id=issue_observation_event_search_fts.rowid WHERE issue_observation_event_search_fts MATCH ? AND events.issue_id=? ORDER BY events.id DESC LIMIT ?`, []any{domain.ContentQueryFTSExpression("projection checkpoint"), "az-1", 10}, "VIRTUAL TABLE INDEX")
	assertPlanContains(`SELECT id FROM issue_observation_events WHERE issue_id=? AND source=? ORDER BY id DESC LIMIT ?`, []any{"az-1", "worker", 10}, "idx_issue_observation_events_issue_source_id")
	assertPlanContains(`SELECT id FROM issue_observation_events AS events WHERE issue_id=? AND json_extract(events.payload_json, '$.outcome') IS ? ORDER BY id DESC LIMIT ?`, []any{"az-1", "accepted", 10}, "idx_issue_observation_events_issue_payload_outcome_id")
}

func TestQueryIssueObservationEventsLargeHistoryReturnsBoundedRecentSearchPage(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	issueID, err := client.Create(ctx, CreateTaskParams{Title: "Large event history", Type: domain.TypeTask, Priority: domain.P2})
	require.NoError(t, err)
	db, err := client.dbHandle()
	require.NoError(t, err)
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	for i := 0; i < 6000; i++ {
		_, err = tx.ExecContext(ctx, `INSERT INTO issue_observation_events(issue_id,event_type,observed_at,source,payload_json) VALUES(?,?,?,?,?)`, issueID, domain.IssueEventProgressRecorded, time.Date(2026, 7, 15, 0, 0, i, 0, time.UTC).Format(time.RFC3339Nano), "load-test", fmt.Sprintf(`{"summary":"projection checkpoint %d","outcome":"accepted"}`, i))
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())

	page, err := client.QueryIssueObservationEvents(ctx, issueID, IssueObservationEventQuery{Order: IssueObservationEventOrderDesc, Limit: 25, Source: "load-test", Query: "projection checkpoint", PayloadEquals: []IssueObservationEventPayloadFilter{{Key: "outcome", Value: "accepted"}}})
	require.NoError(t, err)
	require.Len(t, page.Events, 25)
	assert.True(t, page.HasMore)
	require.NotNil(t, page.NextBeforeID)
	for i := 1; i < len(page.Events); i++ {
		assert.Greater(t, page.Events[i-1].ID, page.Events[i].ID)
	}
}

func TestQueryIssueObservationEventsContinuesPastFTSFalsePositiveChunk(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	issueID, err := client.Create(ctx, CreateTaskParams{Title: "Semantic post-filter", Type: domain.TypeTask, Priority: domain.P2})
	require.NoError(t, err)
	db, err := client.dbHandle()
	require.NoError(t, err)
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	for i := 0; i < 130; i++ {
		_, err = tx.ExecContext(ctx, `INSERT INTO issue_observation_events(issue_id,event_type,observed_at,source,payload_json) VALUES(?,?,?,?,?)`, issueID, domain.IssueEventProgressRecorded, time.Now().UTC().Format(time.RFC3339Nano), "test", `{"summary":"café"}`)
		require.NoError(t, err)
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO issue_observation_events(issue_id,event_type,observed_at,source,payload_json) VALUES(?,?,?,?,?)`, issueID, domain.IssueEventProgressRecorded, time.Now().UTC().Format(time.RFC3339Nano), "test", `{"summary":"cafe checkpoint"}`)
	require.NoError(t, err)
	wantID, err := result.LastInsertId()
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	page, err := client.QueryIssueObservationEvents(ctx, issueID, IssueObservationEventQuery{Order: IssueObservationEventOrderAsc, Limit: 1, Query: "cafe"})
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	assert.Equal(t, wantID, page.Events[0].ID)
}

func TestQueryIssueObservationEventsRejectsUnsafePayloadKeys(t *testing.T) {
	_, err := normalizeIssueObservationEventQuery(IssueObservationEventQuery{PayloadEquals: []IssueObservationEventPayloadFilter{{Key: `outcome') OR 1=1 --`, Value: "accepted"}}})
	require.ErrorContains(t, err, "invalid payload filter key")
}

func TestQueryIssueObservationEventsRejectsUnsupportedPayloadKeys(t *testing.T) {
	_, err := normalizeIssueObservationEventQuery(IssueObservationEventQuery{PayloadEquals: []IssueObservationEventPayloadFilter{{Key: "arbitrary", Value: "value"}}})
	require.ErrorContains(t, err, "invalid payload filter key")
}

func TestNormalizeIssueObservationEventQueryRejectsNegativeLimit(t *testing.T) {
	_, err := normalizeIssueObservationEventQuery(IssueObservationEventQuery{Limit: -1})
	require.ErrorIs(t, err, ErrInvalidIssueObservationEventQuery)
}

func ptrTime(value time.Time) *time.Time { return &value }
