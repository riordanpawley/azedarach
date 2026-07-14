package issues

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestListProjectIssueObservationEventsProvidesDurableCursor(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	writer := newTestClientAtPath(t, path, slog.Default())
	reader := newTestClientAtPath(t, path, slog.Default())
	t.Cleanup(func() { _ = writer.CloseDB(); _ = reader.CloseDB() })
	first, err := writer.Create(ctx, CreateTaskParams{Title: "first", Description: "scope", Acceptance: "done", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	second, err := writer.Create(ctx, CreateTaskParams{Title: "second", Description: "scope", Acceptance: "done", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	events, err := reader.ListProjectIssueObservationEvents(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 || events[0].IssueID.String() != first || events[len(events)-1].IssueID.String() != second {
		t.Fatalf("events = %+v, want project ordered creation events", events)
	}
	cursor := events[0].ID
	after, err := reader.ListProjectIssueObservationEvents(ctx, cursor, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(events)-1 || after[0].ID <= cursor {
		t.Fatalf("after cursor %d = %+v", cursor, after)
	}
}

func TestListLatestIssueObservationEventsByIssueScopesAndRanksCandidates(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	first, err := client.Create(ctx, CreateTaskParams{Title: "first", Description: "scope", Acceptance: "done", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.Create(ctx, CreateTaskParams{Title: "second", Description: "scope", Acceptance: "done", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []IssueObservationEventParams{
		{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{"actor_id": "reviewer", "outcome": "accepted"}},
		{Type: domain.IssueEventReviewCompleted, Source: " daemon-orchestration ", SourceCommand: " review-return ", Payload: map[string]any{"actor_id": "reviewer", "outcome": "returned"}},
		{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-return", Payload: map[string]any{"actor_id": "reviewer", "outcome": "accepted"}},
		{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-return", Payload: map[string]any{"actor_id": 42, "outcome": "returned"}},
	} {
		if _, err := client.AppendIssueObservationEvent(ctx, first, event); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.AppendIssueObservationEvent(ctx, second, IssueObservationEventParams{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{"actor_id": "reviewer", "outcome": "accepted"}}); err != nil {
		t.Fatal(err)
	}
	events, err := client.ListLatestIssueObservationEventsByIssue(ctx, LatestIssueObservationEventOptions{
		IssueIDs:       []string{first},
		Type:           domain.IssueEventReviewCompleted,
		Source:         "daemon-orchestration",
		SourceCommands: []string{"review-accept", "review-return"},
		CommandOutcomePairs: []IssueObservationCommandOutcomePair{
			{SourceCommand: "review-accept", Outcomes: []string{"accepted", "integration_failed"}},
			{SourceCommand: "review-return", Outcomes: []string{"returned"}},
		},
		RequiredPayloadTextKeys: []string{"actor_id"},
		CurrentReviewEpoch:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[first].Payload["outcome"] != "returned" {
		t.Fatalf("latest scoped events = %+v, want newest valid first-issue return only", events)
	}
}

func TestListLatestIssueObservationEventsByIssueStopsAtCurrentReviewEpoch(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, CreateTaskParams{Title: "review", Description: "scope", Acceptance: "done", Type: domain.TypeTask, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppendIssueObservationEvent(ctx, issueID, IssueObservationEventParams{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{"actor_id": "reviewer", "outcome": "accepted"}}); err != nil {
		t.Fatal(err)
	}
	if err := client.Update(ctx, issueID, domain.StatusOpen); err != nil {
		t.Fatal(err)
	}
	if err := client.Update(ctx, issueID, domain.StatusInReview); err != nil {
		t.Fatal(err)
	}
	events, err := client.ListLatestIssueObservationEventsByIssue(ctx, LatestIssueObservationEventOptions{
		IssueIDs: []string{issueID}, Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration",
		CommandOutcomePairs:     []IssueObservationCommandOutcomePair{{SourceCommand: "review-accept", Outcomes: []string{"accepted"}}},
		RequiredPayloadTextKeys: []string{"actor_id"}, CurrentReviewEpoch: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("current epoch outcomes = %+v, want none", events)
	}
}
