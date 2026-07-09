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
