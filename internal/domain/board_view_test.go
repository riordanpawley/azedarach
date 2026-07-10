package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuiltInBoardViewsValidate(t *testing.T) {
	if err := BuiltInBoardViewSet().Validate(); err != nil {
		t.Fatalf("BuiltInBoardViewSet().Validate() error = %v", err)
	}
}

func TestBuiltInBoardViewCatalogAndLegacyAliases(t *testing.T) {
	set := BuiltInBoardViewSet()
	if set.DefaultViewID != BoardViewDefaultID {
		t.Fatalf("default view id = %q, want %q", set.DefaultViewID, BoardViewDefaultID)
	}
	want := []BoardViewID{BoardViewDefaultID, BoardViewPlanningID, BoardViewOrchestrationID, BoardViewCloseoutID}
	if len(set.Views) != len(want) {
		t.Fatalf("built-in views = %d, want %d", len(set.Views), len(want))
	}
	for i, id := range want {
		if set.Views[i].ID != id {
			t.Fatalf("built-in view[%d] = %q, want %q", i, set.Views[i].ID, id)
		}
	}
	if got := NormalizeBoardViewID("current"); got != string(BoardViewDefaultID) {
		t.Fatalf("current alias = %q", got)
	}
	if got := NormalizeBoardViewID("activity"); got != string(BoardViewOrchestrationID) {
		t.Fatalf("activity alias = %q", got)
	}
}

func TestBuiltInBoardViewsUseFocusedFourColumnWorkflows(t *testing.T) {
	tests := []struct {
		view BoardView
		want []BoardColumnID
	}{
		{DefaultBoardView(), []BoardColumnID{BoardColumnOpen, BoardColumnActive, BoardColumnReviewReady, BoardColumnDone}},
		{PlanningBoardView(), []BoardColumnID{BoardColumnBacklog, BoardColumnOpen, BoardColumnActive, BoardColumnReviewReady}},
		{OrchestrationBoardView(), []BoardColumnID{BoardColumnWaitingHuman, BoardColumnWaitingAI, BoardColumnActive, BoardColumnReviewReady}},
		{CloseoutBoardView(), []BoardColumnID{BoardColumnReviewReady, BoardColumnDone, BoardColumnCancelled}},
	}
	for _, tt := range tests {
		t.Run(tt.view.Title, func(t *testing.T) {
			if len(tt.view.Columns) != len(tt.want) {
				t.Fatalf("columns = %d, want %d", len(tt.view.Columns), len(tt.want))
			}
			for i, want := range tt.want {
				if got := tt.view.Columns[i].ID; got != want {
					t.Fatalf("column[%d] = %q, want %q", i, got, want)
				}
			}
		})
	}
}

func TestOrchestrationBoardViewPlacesWaitingBeforeWorking(t *testing.T) {
	task := Task{ID: "az-waiting", Status: StatusInProgress, Session: &Session{Activity: "waiting-for-human"}, HasTmuxSession: true}
	placement, err := OrchestrationBoardView().PlaceTask(task)
	if err != nil {
		t.Fatalf("PlaceTask error: %v", err)
	}
	if placement.ColumnID != BoardColumnWaitingHuman {
		t.Fatalf("column = %q, want %q", placement.ColumnID, BoardColumnWaitingHuman)
	}
}

func TestFocusedBoardViewsLeaveOutOfScopeIssuesUnmatched(t *testing.T) {
	backlogState, err := NewIssueState(IssueStateParts{Workflow: IssueWorkflowBacklog})
	if err != nil {
		t.Fatalf("NewIssueState backlog error: %v", err)
	}
	tests := []struct {
		name string
		view BoardView
		task Task
	}{
		{name: "default backlog", view: DefaultBoardView(), task: Task{ID: "az-backlog", Status: StatusOpen, State: backlogState}},
		{name: "default cancelled", view: DefaultBoardView(), task: Task{ID: "az-cancelled", Status: StatusCancelled}},
		{name: "planning done", view: PlanningBoardView(), task: Task{ID: "az-done", Status: StatusDone}},
		{name: "planning cancelled", view: PlanningBoardView(), task: Task{ID: "az-cancelled", Status: StatusCancelled}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			placement, err := tt.view.PlaceTask(tt.task)
			if err != nil {
				t.Fatalf("PlaceTask error: %v", err)
			}
			if placement.Matched {
				t.Fatalf("placement = %+v, want unmatched", placement)
			}
		})
	}
}

func TestCloseoutBoardViewLeavesNonCloseoutIssuesUnmatched(t *testing.T) {
	placement, err := CloseoutBoardView().PlaceTask(Task{ID: "az-open", Status: StatusOpen, Priority: P2, Type: TypeTask})
	if err != nil {
		t.Fatalf("PlaceTask error: %v", err)
	}
	if placement.Matched {
		t.Fatalf("open placement = %+v, want unmatched", placement)
	}
}

func TestBoardViewValidationRejectsUnknownPredicate(t *testing.T) {
	view := DefaultBoardView()
	view.ID = "custom"
	view.Title = "Custom"
	view.Columns[0].Predicates[0].Kind = "future_kind"

	if err := view.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want unknown predicate error")
	}
}

func TestGroupTasksByBoardViewUsesTypedPlacementAndFirstMatch(t *testing.T) {
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
	active := findBoardViewTestColumn(columns, BoardColumnActive)
	if len(active.Tasks) != 1 || active.Tasks[0].ID != "az-active-review" {
		t.Fatalf("active column tasks = %+v, want busy review task", active.Tasks)
	}
	review := findBoardViewTestColumn(columns, BoardColumnReviewReady)
	if len(review.Tasks) != 0 {
		t.Fatalf("review column tasks = %+v, want none", review.Tasks)
	}
	done := findBoardViewTestColumn(columns, BoardColumnDone)
	if len(done.Tasks) != 1 || done.Tasks[0].ID != "az-done" {
		t.Fatalf("done column tasks = %+v, want done task", done.Tasks)
	}
}

func TestBoardViewPlacementMatchesTypedPredicates(t *testing.T) {
	view := BoardView{
		ID:    "test",
		Title: "Test",
		Columns: []BoardColumn{
			{
				ID:    BoardColumnWaitingHuman,
				Title: "Waiting Human",
				Predicates: []BoardColumnPredicate{{
					Kind: BoardPredicateWaitingHuman,
				}},
			},
			{
				ID:    BoardColumnWaitingAI,
				Title: "Waiting AI",
				Predicates: []BoardColumnPredicate{{
					Kind: BoardPredicateWaitingAIDelegated,
				}},
			},
			{
				ID:    BoardColumnReviewReady,
				Title: "Review Ready",
				Predicates: []BoardColumnPredicate{{
					Kind: BoardPredicateReviewReady,
				}},
			},
			{
				ID:    BoardColumnDone,
				Title: "Done",
				Predicates: []BoardColumnPredicate{{
					Kind:           BoardPredicateClosedOutcome,
					ClosedOutcomes: []IssueCloseOutcome{IssueCloseCompleted},
				}},
			},
			{
				ID:    BoardColumnCancelled,
				Title: "Cancelled",
				Predicates: []BoardColumnPredicate{{
					Kind:           BoardPredicateClosedOutcome,
					ClosedOutcomes: []IssueCloseOutcome{IssueCloseCancelled},
				}},
			},
			{
				ID:    BoardColumnOpen,
				Title: "Open",
				Predicates: []BoardColumnPredicate{{
					Kind:      BoardPredicateLifecycle,
					Lifecycle: []IssueWorkflow{IssueWorkflowOpen},
				}},
			},
		},
	}

	tests := []struct {
		name       string
		task       Task
		wantColumn BoardColumnID
		wantReason string
	}{
		{
			name: "lifecycle open",
			task: Task{
				ID:       "az-open",
				Status:   StatusOpen,
				Priority: P2,
				Type:     TypeTask,
			},
			wantColumn: BoardColumnOpen,
			wantReason: "lifecycle=open",
		},
		{
			name: "review ready",
			task: Task{
				ID:       "az-review",
				Status:   StatusInReview,
				Priority: P2,
				Type:     TypeTask,
				Session:  &Session{Activity: string(SessionIdle)},
			},
			wantColumn: BoardColumnReviewReady,
			wantReason: "review_ready=true",
		},
		{
			name: "review ready from first-class issue state",
			task: Task{
				ID: "az-review-state",
				State: mustIssueState(t, IssueStateParts{
					Workflow: IssueWorkflowActive,
					Review:   IssueReviewRequested,
				}),
				Status:   StatusOpen,
				Priority: P2,
				Type:     TypeTask,
				Session:  &Session{Activity: "no-agent"},
			},
			wantColumn: BoardColumnReviewReady,
			wantReason: "review_ready=true",
		},
		{
			name: "closed completed outcome",
			task: Task{
				ID:       "az-done",
				Status:   StatusDone,
				Priority: P2,
				Type:     TypeTask,
			},
			wantColumn: BoardColumnDone,
			wantReason: "closed_outcome=completed",
		},
		{
			name: "closed cancelled outcome",
			task: Task{
				ID:       "az-cancelled",
				Status:   StatusCancelled,
				Priority: P2,
				Type:     TypeTask,
			},
			wantColumn: BoardColumnCancelled,
			wantReason: "closed_outcome=cancelled",
		},
		{
			name: "waiting human",
			task: Task{
				ID:       "az-human",
				Status:   StatusInReview,
				Priority: P2,
				Type:     TypeTask,
				Session:  &Session{Activity: "waiting-for-human"},
			},
			wantColumn: BoardColumnWaitingHuman,
			wantReason: "waiting_human=true",
		},
		{
			name: "generic waiting state is human waiting",
			task: Task{
				ID:       "az-waiting",
				Status:   StatusInProgress,
				Priority: P2,
				Type:     TypeTask,
				Session:  &Session{State: SessionWaiting},
			},
			wantColumn: BoardColumnWaitingHuman,
			wantReason: "waiting_human=true",
		},
		{
			name: "waiting ai delegated operation",
			task: Task{
				ID:       "az-ai",
				Status:   StatusInProgress,
				Priority: P2,
				Type:     TypeTask,
				Session:  &Session{Activity: "waiting_tool"},
			},
			wantColumn: BoardColumnWaitingAI,
			wantReason: "waiting_ai_delegated=true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			placement, err := view.PlaceTask(tt.task)
			if err != nil {
				t.Fatalf("PlaceTask() error = %v", err)
			}
			if !placement.Matched {
				t.Fatalf("PlaceTask() did not match: %+v", placement)
			}
			if placement.ColumnID != tt.wantColumn {
				t.Fatalf("ColumnID = %s, want %s; placement=%+v", placement.ColumnID, tt.wantColumn, placement)
			}
			if !strings.Contains(placement.MatchReason, tt.wantReason) {
				t.Fatalf("MatchReason = %q, want to contain %q", placement.MatchReason, tt.wantReason)
			}
		})
	}
}

func TestBoardViewPlacementFirstMatchWins(t *testing.T) {
	view := BoardView{
		ID:    "precedence",
		Title: "Precedence",
		Columns: []BoardColumn{
			{
				ID:    BoardColumnActive,
				Title: "Active Lifecycle",
				Predicates: []BoardColumnPredicate{{
					Kind:      BoardPredicateLifecycle,
					Lifecycle: []IssueWorkflow{IssueWorkflowActive},
				}},
			},
			{
				ID:    BoardColumnReviewReady,
				Title: "Active Display",
				Predicates: []BoardColumnPredicate{{
					Kind:          BoardPredicateDisplayPhase,
					DisplayPhases: []IssueDisplayPhase{IssueDisplayActive},
				}},
			},
		},
	}
	placement, err := view.PlaceTask(Task{
		ID:       "az-active",
		Status:   StatusInProgress,
		Priority: P2,
		Type:     TypeTask,
	})
	if err != nil {
		t.Fatalf("PlaceTask() error = %v", err)
	}
	if placement.ColumnID != BoardColumnActive || placement.ColumnIndex != 0 {
		t.Fatalf("placement = %+v, want first matching column", placement)
	}
}

func TestBoardViewValidationRejectsInvalidDefinitions(t *testing.T) {
	validColumn := BoardColumn{
		ID:    BoardColumnOpen,
		Title: "Open",
		Predicates: []BoardColumnPredicate{{
			Kind:      BoardPredicateLifecycle,
			Lifecycle: []IssueWorkflow{IssueWorkflowOpen},
		}},
	}

	tests := []struct {
		name string
		view BoardView
		want string
	}{
		{
			name: "empty columns",
			view: BoardView{ID: "empty", Title: "Empty"},
			want: "at least one column",
		},
		{
			name: "empty column id",
			view: BoardView{ID: "empty-column", Title: "Empty Column", Columns: []BoardColumn{{
				Title: "Open",
				Predicates: []BoardColumnPredicate{{
					Kind:      BoardPredicateLifecycle,
					Lifecycle: []IssueWorkflow{IssueWorkflowOpen},
				}},
			}}},
			want: "column id is required",
		},
		{
			name: "duplicate column ids",
			view: BoardView{ID: "dupe", Title: "Duplicate", Columns: []BoardColumn{validColumn, validColumn}},
			want: "duplicate board column id",
		},
		{
			name: "unknown column id",
			view: BoardView{ID: "unknown", Title: "Unknown", Columns: []BoardColumn{{
				ID:    "unknown",
				Title: "Unknown",
				Predicates: []BoardColumnPredicate{{
					Kind: BoardColumnPredicateKind("sql"),
				}},
			}}},
			want: "unknown board column id",
		},
		{
			name: "unsupported predicate kind",
			view: BoardView{ID: "bad-predicate", Title: "Bad Predicate", Columns: []BoardColumn{{
				ID:    BoardColumnOpen,
				Title: "Open",
				Predicates: []BoardColumnPredicate{{
					Kind: BoardColumnPredicateKind("sql"),
				}},
			}}},
			want: "unsupported board column predicate kind",
		},
		{
			name: "impossible predicate combination",
			view: BoardView{ID: "impossible", Title: "Impossible", Columns: []BoardColumn{{
				ID:    BoardColumnDone,
				Title: "Impossible",
				Predicates: []BoardColumnPredicate{
					{Kind: BoardPredicateLifecycle, Lifecycle: []IssueWorkflow{IssueWorkflowOpen}},
					{Kind: BoardPredicateClosedOutcome, ClosedOutcomes: []IssueCloseOutcome{IssueCloseCompleted}},
				},
			}}},
			want: "closed outcome requires lifecycle closed",
		},
		{
			name: "closed outcome display phase contradiction",
			view: BoardView{ID: "contradiction", Title: "Contradiction", Columns: []BoardColumn{{
				ID:    BoardColumnDone,
				Title: "Contradiction",
				Predicates: []BoardColumnPredicate{
					{Kind: BoardPredicateDisplayPhase, DisplayPhases: []IssueDisplayPhase{IssueDisplayActive}},
					{Kind: BoardPredicateClosedOutcome, ClosedOutcomes: []IssueCloseOutcome{IssueCloseCompleted}},
				},
			}}},
			want: "closed outcome and display phase predicates cannot match",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.view.Validate()
			if err == nil {
				t.Fatal("Validate() error = nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %q, want to contain %q", err, tt.want)
			}
		})
	}
}

func TestBoardViewSetValidationRejectsDuplicateAndUnknownIDs(t *testing.T) {
	current := CurrentBoardView()
	tests := []struct {
		name string
		set  BoardViewSet
		want string
	}{
		{
			name: "duplicate view ids",
			set:  BoardViewSet{DefaultViewID: current.ID, Views: []BoardView{current, current}},
			want: "duplicate board view id",
		},
		{
			name: "unknown default view",
			set:  BoardViewSet{DefaultViewID: "missing", Views: []BoardView{current}},
			want: "default board view",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.set.Validate()
			if err == nil {
				t.Fatal("Validate() error = nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %q, want to contain %q", err, tt.want)
			}
		})
	}
}

func TestBoardColumnPredicateJSONRejectsInvalidDefinitions(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "unsupported predicate kind cannot decode",
			body: `{"kind":"sql"}`,
			want: "unsupported board column predicate kind",
		},
		{
			name: "sql fragment payload cannot decode",
			body: `{"kind":"lifecycle","lifecycle":["open"],"sql":"status = 'open'"}`,
			want: "unknown field",
		},
		{
			name: "wrong payload for kind cannot decode",
			body: `{"kind":"review_ready","lifecycle":["active"]}`,
			want: "cannot carry payload",
		},
		{
			name: "unknown lifecycle cannot decode",
			body: `{"kind":"lifecycle","lifecycle":["triage"]}`,
			want: "invalid issue workflow",
		},
		{
			name: "empty closed outcome cannot decode",
			body: `{"kind":"closed_outcome","closed_outcomes":[]}`,
			want: "requires at least one outcome",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var predicate BoardColumnPredicate
			err := json.Unmarshal([]byte(tt.body), &predicate)
			if err == nil {
				t.Fatal("json.Unmarshal() error = nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("json.Unmarshal() error = %q, want to contain %q", err, tt.want)
			}
		})
	}
}

func findBoardViewTestColumn(columns []BoardViewColumnSnapshot, id BoardColumnID) BoardViewColumnSnapshot {
	for _, column := range columns {
		if column.Definition.ID == id {
			return column
		}
	}
	return BoardViewColumnSnapshot{}
}
