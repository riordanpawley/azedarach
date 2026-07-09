package domain

import "testing"

func TestBoardViewDefinitionValidationRejectsUnknownPredicate(t *testing.T) {
	view := DefaultBoardView()
	view.ID = "custom"
	view.Name = "Custom"
	view.Columns[0].Predicate.Type = "future_kind"

	if err := view.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want unknown predicate error")
	}
}

func TestGroupTasksByBoardViewUsesDisplayPhaseAndFirstMatch(t *testing.T) {
	reviewState, err := NewIssueState(IssueStateParts{
		Workflow:     IssueWorkflowActive,
		Review:       IssueReviewRequested,
		CloseOutcome: IssueCloseNone,
		Archive:      IssueArchiveLive,
		Deletion:     IssueDeletionPresent,
	})
	if err != nil {
		t.Fatalf("new review state: %v", err)
	}
	doneState, err := NewIssueState(IssueStateParts{
		Workflow:     IssueWorkflowClosed,
		Review:       IssueReviewNone,
		CloseOutcome: IssueCloseCompleted,
		Archive:      IssueArchiveLive,
		Deletion:     IssueDeletionPresent,
	})
	if err != nil {
		t.Fatalf("new done state: %v", err)
	}
	tasks := []Task{
		{ID: "az-active-review", Title: "Busy review", Status: StatusInReview, State: reviewState, Session: &Session{Activity: "busy"}, HasTmuxSession: true},
		{ID: "az-done", Title: "Done", Status: StatusDone, State: doneState},
	}

	columns, err := GroupTasksByBoardView(DefaultBoardView(), tasks)
	if err != nil {
		t.Fatalf("GroupTasksByBoardView error: %v", err)
	}
	active := findBoardViewTestColumn(columns, string(IssueDisplayActive))
	if len(active.Tasks) != 1 || active.Tasks[0].ID != "az-active-review" {
		t.Fatalf("active column tasks = %+v, want busy review task", active.Tasks)
	}
	review := findBoardViewTestColumn(columns, string(IssueDisplayReview))
	if len(review.Tasks) != 0 {
		t.Fatalf("review column tasks = %+v, want none", review.Tasks)
	}
	done := findBoardViewTestColumn(columns, string(IssueDisplayDone))
	if len(done.Tasks) != 1 || done.Tasks[0].ID != "az-done" {
		t.Fatalf("done column tasks = %+v, want done task", done.Tasks)
	}
}

func findBoardViewTestColumn(columns []BoardViewColumnSnapshot, id string) BoardViewColumnSnapshot {
	for _, column := range columns {
		if column.Definition.ID == id {
			return column
		}
	}
	return BoardViewColumnSnapshot{}
}
