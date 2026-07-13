package domain

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWaitingHumanPredicateDistinguishesDecisionFromRuntimePrompt(t *testing.T) {
	p := BoardColumnPredicate{Kind: BoardPredicateWaitingHuman}
	decision := Task{Facts: DeriveIssueFacts(IssueFactsInput{Status: StatusOpen, Priority: P2, DecisionWaiting: true, DecisionWaitReason: "choose region"})}
	if ok, reason := p.MatchTask(decision); !ok || reason != "waiting_human=true source=interaction_request reason=choose region" {
		t.Fatalf("decision match = %v, %q", ok, reason)
	}
	runtime := Task{Status: StatusInProgress, Priority: P2, Session: &Session{Activity: "waiting-for-human"}}
	if ok, reason := p.MatchTask(runtime); !ok || reason != "waiting_human=true source=runtime_prompt reason=active session is waiting for human input" {
		t.Fatalf("runtime match = %v, %q", ok, reason)
	}
}

func TestBuiltInBoardViewsValidate(t *testing.T) {
	set := BuiltInBoardViewSet()
	if err := set.Validate(); err != nil {
		t.Fatalf("BuiltInBoardViewSet().Validate() error = %v", err)
	}
	for _, view := range set.Views {
		if view.Options.SortPolicy != BoardViewSortHumanAttention {
			t.Fatalf("built-in view %q sort policy = %q, want %q", view.ID, view.Options.SortPolicy, BoardViewSortHumanAttention)
		}
	}
}

func TestBoardViewValidationRejectsUnknownSortPolicy(t *testing.T) {
	view := DefaultBoardView()
	view.Options.SortPolicy = "future_policy"
	if err := view.Validate(); err == nil || !strings.Contains(err.Error(), "unsupported sort policy") {
		t.Fatalf("Validate() error = %v, want unsupported sort policy", err)
	}
}

func TestBoardViewSortPolicyJSONRoundTrip(t *testing.T) {
	view := DefaultBoardView()
	encoded, err := EncodeBoardViewDefinitionJSON(view)
	if err != nil {
		t.Fatalf("EncodeBoardViewDefinitionJSON: %v", err)
	}
	if !strings.Contains(string(encoded), `"sort_policy":"human_attention"`) {
		t.Fatalf("encoded definition missing sort policy: %s", encoded)
	}
	decoded, err := DecodeBoardViewDefinitionJSON(encoded)
	if err != nil {
		t.Fatalf("DecodeBoardViewDefinitionJSON: %v", err)
	}
	if decoded.Options.SortPolicy != BoardViewSortHumanAttention {
		t.Fatalf("decoded sort policy = %q", decoded.Options.SortPolicy)
	}
}

func TestBoardViewSchemaV1MigratesToColumnLayoutAndAttentionSort(t *testing.T) {
	legacy := []byte(`{"schema_version":1,"id":"legacy","title":"Legacy","columns":[{"id":"active","title":"Active","predicates":[{"kind":"lifecycle","lifecycle":["active"]}]}],"options":{"sort_policy":"human_attention"}}`)
	view, err := DecodeBoardViewDefinitionJSON(legacy)
	if err != nil {
		t.Fatalf("DecodeBoardViewDefinitionJSON(v1): %v", err)
	}
	if view.Layout != BoardViewLayoutColumnBoard {
		t.Fatalf("layout = %q, want %q", view.Layout, BoardViewLayoutColumnBoard)
	}
	if len(view.Sort) != 1 || view.Sort[0].Key != BoardViewSortKeyHumanAttention {
		t.Fatalf("sort = %#v, want migrated human-attention rule", view.Sort)
	}
	encoded, err := EncodeBoardViewDefinitionJSON(view)
	if err != nil {
		t.Fatalf("EncodeBoardViewDefinitionJSON: %v", err)
	}
	if !bytes.Contains(encoded, []byte(`"schema_version":2`)) {
		t.Fatalf("encoded definition = %s, want schema v2", encoded)
	}
}

func TestProjectTasksByBoardViewAppliesFiltersOrderedRulesAndTreeLayout(t *testing.T) {
	now := time.Now().UTC()
	parentID := Task{ID: "parent"}.ID
	view := DefaultBoardView()
	view.Layout = BoardViewLayoutTreeList
	view.Filters = []BoardColumnPredicate{{Kind: BoardPredicateLifecycle, Lifecycle: []IssueWorkflow{IssueWorkflowActive}}}
	view.Sort = []BoardViewSortRule{
		{Key: BoardViewSortKeyPriority, Direction: BoardViewSortAscending},
		{Key: BoardViewSortKeyUpdated, Direction: BoardViewSortDescending},
	}
	tasks := []Task{
		{ID: "child", ParentID: &parentID, Status: StatusInProgress, Priority: P0, UpdatedAt: now},
		{ID: parentID, Status: StatusInProgress, Priority: P2, UpdatedAt: now.Add(-time.Hour)},
		{ID: "filtered", Status: StatusOpen, Priority: P0, UpdatedAt: now.Add(time.Hour)},
	}
	projection, err := ProjectTasksByBoardView(view, tasks)
	if err != nil {
		t.Fatalf("ProjectTasksByBoardView: %v", err)
	}
	if projection.View.Layout != BoardViewLayoutTreeList {
		t.Fatalf("layout = %q", projection.View.Layout)
	}
	if got := taskIDs(projection.OrderedTasks()); !slices.Equal(got, []string{"parent", "child"}) {
		t.Fatalf("ordered = %v, want parent-before-child and filtered task omitted", got)
	}
}

func TestTreeProjectionPromotesChildWhenParentIsFiltered(t *testing.T) {
	parentID := Task{ID: "parent"}.ID
	view := DefaultBoardView()
	view.Layout = BoardViewLayoutTreeList
	view.Filters = []BoardColumnPredicate{{Kind: BoardPredicateLifecycle, Lifecycle: []IssueWorkflow{IssueWorkflowActive}}}
	projection, err := ProjectTasksByBoardView(view, []Task{
		{ID: parentID, Status: StatusOpen},
		{ID: "child", ParentID: &parentID, Status: StatusInProgress},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Items) != 1 || projection.Items[0].Task.ID != "child" || projection.Items[0].Depth != 0 {
		t.Fatalf("projection items = %+v, want child promoted to root", projection.Items)
	}
}

func TestBoardViewAllowsCustomGroupIDs(t *testing.T) {
	view := DefaultBoardView()
	view.Columns[0].ID = "needs_attention"
	if err := view.Validate(); err != nil {
		t.Fatalf("custom group id rejected: %v", err)
	}
}

func TestCustomGroupIDDoesNotUseLegacyViewAliases(t *testing.T) {
	column := DefaultBoardView().Columns[0]
	column.ID = "current"
	if got := column.Normalized().ID; got != "current" {
		t.Fatalf("group id = %q, want current", got)
	}
}

func taskIDs(tasks []Task) []string {
	out := make([]string, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, task.ID.String())
	}
	return out
}

func TestBuiltInBoardViewCatalogAndLegacyAliases(t *testing.T) {
	set := BuiltInBoardViewSet()
	if set.DefaultViewID != BoardViewDefaultID {
		t.Fatalf("default view id = %q, want %q", set.DefaultViewID, BoardViewDefaultID)
	}
	want := []BoardViewID{BoardViewDefaultID, BoardViewPlanningID, BoardViewOrchestrationID, BoardViewCloseoutID, BoardViewGridID, BoardViewTreeID}
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

func TestBuiltInViewsExposeAllSupportedLayouts(t *testing.T) {
	want := map[BoardViewLayout]bool{
		BoardViewLayoutColumnBoard:    true,
		BoardViewLayoutHorizontalGrid: true,
		BoardViewLayoutTreeList:       true,
	}
	for _, view := range BuiltInBoardViewSet().Views {
		delete(want, view.Normalized().Layout)
	}
	if len(want) != 0 {
		t.Fatalf("built-in catalog missing layouts: %v", want)
	}
}

func TestLegacyUIViewModesMigrateToConfiguredViews(t *testing.T) {
	tests := map[string]string{"board": string(BoardViewDefaultID), "compact": string(BoardViewTreeID), "overview": string(BoardViewOrchestrationID)}
	for legacy, want := range tests {
		if got, ok := BoardViewIDFromLegacyUIMode(legacy); !ok || got != want {
			t.Fatalf("BoardViewIDFromLegacyUIMode(%q) = %q, %t; want %q, true", legacy, got, ok, want)
		}
	}
}

func TestProjectionExposesOrchestrationViewState(t *testing.T) {
	task := Task{ID: "waiting", Title: "Waiting", Status: StatusInProgress, Session: &Session{Activity: "waiting-for-human"}}
	projection, err := ProjectTasksByBoardView(DefaultBoardView(), []Task{task})
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Items) != 1 || projection.Items[0].OrchestrationState != OrchestrationViewWaitingHuman {
		t.Fatalf("projection items = %#v", projection.Items)
	}
}

func TestBuiltInBoardViewsUseFocusedWorkflows(t *testing.T) {
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

func TestOrchestrationBoardViewSeparatesHumanAuthorityFromReviewReadiness(t *testing.T) {
	view := OrchestrationBoardView()
	tests := []struct {
		name       string
		task       Task
		wantColumn BoardColumnID
		wantMatch  bool
	}{
		{name: "ordinary review", task: Task{ID: "review", Status: StatusInReview, Session: &Session{Activity: string(SessionIdle)}, HasTmuxSession: true}, wantColumn: BoardColumnReviewReady, wantMatch: true},
		{name: "human authority wins over review", task: Task{ID: "human", Status: StatusInReview, Facts: IssueFacts{LifecycleState: IssueWorkflowActive, DisplayPhase: IssueDisplayReview, ReviewReadyVisible: true, WaitingHuman: true, WaitingHumanSource: WaitingHumanSourceInvestigationAcceptance, WaitingHumanReason: "explicit acceptance required"}}, wantColumn: BoardColumnWaitingHuman, wantMatch: true},
		{name: "ai review", task: Task{ID: "ai", Status: StatusInProgress, Session: &Session{Activity: "waiting_tool"}, HasTmuxSession: true}, wantColumn: BoardColumnWaitingAI, wantMatch: true},
		{name: "in progress", task: Task{ID: "active", Status: StatusInProgress, Session: &Session{Activity: string(SessionBusy)}, HasTmuxSession: true}, wantColumn: BoardColumnActive, wantMatch: true},
		{name: "open omitted", task: Task{ID: "open", Status: StatusOpen}, wantMatch: false},
		{name: "done omitted", task: Task{ID: "done", Status: StatusDone}, wantMatch: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			placement, err := view.PlaceTask(tt.task)
			if err != nil {
				t.Fatalf("PlaceTask error: %v", err)
			}
			if placement.Matched != tt.wantMatch || placement.ColumnID != tt.wantColumn {
				t.Fatalf("placement = %+v, want matched=%t column=%q", placement, tt.wantMatch, tt.wantColumn)
			}
		})
	}
}

func TestTreeBoardViewSortsHumanAttentionRootsFirst(t *testing.T) {
	projection, err := ProjectTasksByBoardView(TreeBoardView(), []Task{
		{ID: "ordinary", Status: StatusInProgress, Priority: P0, UpdatedAt: time.Now().UTC()},
		{ID: "review", Status: StatusInReview, Priority: P4, Session: &Session{Activity: string(SessionIdle)}, HasTmuxSession: true},
		{ID: "waiting", Status: StatusInProgress, Priority: P4, Session: &Session{Activity: "waiting-for-human"}, HasTmuxSession: true},
	})
	if err != nil {
		t.Fatalf("ProjectTasksByBoardView: %v", err)
	}
	if got := taskIDs(projection.OrderedTasks()); !slices.Equal(got, []string{"waiting", "review", "ordinary"}) {
		t.Fatalf("tree order = %v, want waiting-human then review then ordinary", got)
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
	})
	if err != nil {
		t.Fatalf("new review state: %v", err)
	}
	doneState, err := NewIssueState(IssueStateParts{
		Workflow:     IssueWorkflowClosed,
		Review:       IssueReviewNone,
		CloseOutcome: IssueCloseCompleted,
		Archive:      IssueArchiveLive,
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

func TestGroupTasksByBuiltInBoardViewAppliesStableAttentionPolicy(t *testing.T) {
	tasks := []Task{
		{ID: "ordinary-newer", Status: StatusInProgress},
		{ID: "ordinary-older", Status: StatusInProgress},
		{ID: "review", Status: StatusInReview},
		{ID: "waiting", Status: StatusInProgress, Session: &Session{Activity: "waiting-for-human"}},
	}
	columns, err := GroupTasksByBoardView(DefaultBoardView(), tasks)
	if err != nil {
		t.Fatalf("GroupTasksByBoardView: %v", err)
	}
	active := findBoardViewTestColumn(columns, BoardColumnActive).Tasks
	if len(active) != 3 || active[0].ID != "waiting" || active[1].ID != "ordinary-newer" || active[2].ID != "ordinary-older" {
		t.Fatalf("active order = %+v, want attention first with stable ordinary order", active)
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
			want: "unsupported board column predicate kind",
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
