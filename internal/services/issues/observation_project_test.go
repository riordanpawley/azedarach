package issues

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestListProjectIssueObservationEventsProvidesDurableCursor(t *testing.T) {
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
