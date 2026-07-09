package domain

import "testing"

func TestIssueStateFromLegacyMapsWorkflowReviewAndCloseState(t *testing.T) {
	tests := []struct {
		name       string
		input      LegacyIssueStateInput
		workflow   IssueWorkflow
		review     IssueReviewState
		close      IssueCloseOutcome
		boardPhase IssueBoardPhase
		display    IssueDisplayPhase
		archive    IssueArchiveState
		deletion   IssueDeletionState
	}{
		{
			name:       "p4 open issue is backlog",
			input:      LegacyIssueStateInput{Status: StatusOpen, Priority: P4},
			workflow:   IssueWorkflowBacklog,
			review:     IssueReviewNone,
			close:      IssueCloseNone,
			boardPhase: IssueBoardBacklog,
			display:    IssueDisplayBacklog,
			archive:    IssueArchiveLive,
			deletion:   IssueDeletionPresent,
		},
		{
			name:       "non backlog open issue is open",
			input:      LegacyIssueStateInput{Status: StatusOpen, Priority: P2},
			workflow:   IssueWorkflowOpen,
			review:     IssueReviewNone,
			close:      IssueCloseNone,
			boardPhase: IssueBoardOpen,
			display:    IssueDisplayOpen,
			archive:    IssueArchiveLive,
			deletion:   IssueDeletionPresent,
		},
		{
			name:       "in progress issue is active",
			input:      LegacyIssueStateInput{Status: StatusInProgress, Priority: P1},
			workflow:   IssueWorkflowActive,
			review:     IssueReviewNone,
			close:      IssueCloseNone,
			boardPhase: IssueBoardActive,
			display:    IssueDisplayActive,
			archive:    IssueArchiveLive,
			deletion:   IssueDeletionPresent,
		},
		{
			name:       "in review issue is active with review requested",
			input:      LegacyIssueStateInput{Status: StatusInReview, Priority: P1},
			workflow:   IssueWorkflowActive,
			review:     IssueReviewRequested,
			close:      IssueCloseNone,
			boardPhase: IssueBoardReview,
			display:    IssueDisplayReview,
			archive:    IssueArchiveLive,
			deletion:   IssueDeletionPresent,
		},
		{
			name:       "closed issue defaults to completed",
			input:      LegacyIssueStateInput{Status: StatusDone, Priority: P1},
			workflow:   IssueWorkflowClosed,
			review:     IssueReviewNone,
			close:      IssueCloseCompleted,
			boardPhase: IssueBoardClosed,
			display:    IssueDisplayDone,
			archive:    IssueArchiveLive,
			deletion:   IssueDeletionPresent,
		},
		{
			name:       "cancelled legacy status maps to cancelled closure",
			input:      LegacyIssueStateInput{Status: StatusCancelled, Priority: P1},
			workflow:   IssueWorkflowClosed,
			review:     IssueReviewNone,
			close:      IssueCloseCancelled,
			boardPhase: IssueBoardClosed,
			display:    IssueDisplayCancelled,
			archive:    IssueArchiveLive,
			deletion:   IssueDeletionPresent,
		},
		{
			name:       "cancelled flag overrides closed outcome",
			input:      LegacyIssueStateInput{Status: StatusDone, Priority: P1, Cancelled: true},
			workflow:   IssueWorkflowClosed,
			review:     IssueReviewNone,
			close:      IssueCloseCancelled,
			boardPhase: IssueBoardClosed,
			display:    IssueDisplayCancelled,
			archive:    IssueArchiveLive,
			deletion:   IssueDeletionPresent,
		},
		{
			name:       "archive and tombstone are represented separately",
			input:      LegacyIssueStateInput{Status: StatusOpen, Priority: P2, Archived: true, Tombstoned: true},
			workflow:   IssueWorkflowOpen,
			review:     IssueReviewNone,
			close:      IssueCloseNone,
			boardPhase: IssueBoardOpen,
			display:    IssueDisplayOpen,
			archive:    IssueArchiveArchived,
			deletion:   IssueDeletionTombstoned,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := IssueStateFromLegacy(tt.input)
			if err != nil {
				t.Fatalf("IssueStateFromLegacy() error = %v", err)
			}
			if got.Workflow() != tt.workflow {
				t.Errorf("Workflow() = %s, want %s", got.Workflow(), tt.workflow)
			}
			if got.Review() != tt.review {
				t.Errorf("Review() = %s, want %s", got.Review(), tt.review)
			}
			if got.CloseOutcome() != tt.close {
				t.Errorf("CloseOutcome() = %s, want %s", got.CloseOutcome(), tt.close)
			}
			if got.BoardPhase() != tt.boardPhase {
				t.Errorf("BoardPhase() = %s, want %s", got.BoardPhase(), tt.boardPhase)
			}
			if got.DisplayPhase() != tt.display {
				t.Errorf("DisplayPhase() = %s, want %s", got.DisplayPhase(), tt.display)
			}
			if got.Archive() != tt.archive {
				t.Errorf("Archive() = %s, want %s", got.Archive(), tt.archive)
			}
			if got.Deletion() != tt.deletion {
				t.Errorf("Deletion() = %s, want %s", got.Deletion(), tt.deletion)
			}
		})
	}
}

func TestIssueStateFromLegacyRejectsUnknownStatus(t *testing.T) {
	_, err := IssueStateFromLegacy(LegacyIssueStateInput{Status: Status("blocked"), Priority: P2})
	if err == nil {
		t.Fatal("IssueStateFromLegacy() error = nil, want invalid status error")
	}
}

func TestIssueStateBoardPhaseReturnsUnknownForInvalidState(t *testing.T) {
	if got := (IssueState{}).BoardPhase(); got != IssueBoardUnknown {
		t.Fatalf("BoardPhase() = %s, want %s", got, IssueBoardUnknown)
	}
	if got := (IssueState{}).DisplayPhase(); got != IssueDisplayUnknown {
		t.Fatalf("DisplayPhase() = %s, want %s", got, IssueDisplayUnknown)
	}
}

func TestTaskIssueDisplayPhaseDerivesReviewFromSessionActivity(t *testing.T) {
	tests := []struct {
		name string
		task Task
		want IssueDisplayPhase
	}{
		{
			name: "backlog is derived from open P4",
			task: Task{Status: StatusOpen, Priority: P4},
			want: IssueDisplayBacklog,
		},
		{
			name: "cancelled status is separate from done",
			task: Task{Status: StatusCancelled, Priority: P2},
			want: IssueDisplayCancelled,
		},
		{
			name: "idle review handoff is review",
			task: Task{Status: StatusInReview, Priority: P2, Session: &Session{Activity: string(SessionIdle)}},
			want: IssueDisplayReview,
		},
		{
			name: "waiting review handoff remains active",
			task: Task{Status: StatusInReview, Priority: P2, Session: &Session{Activity: string(SessionWaiting)}},
			want: IssueDisplayActive,
		},
		{
			name: "waiting for human review handoff remains active",
			task: Task{Status: StatusInReview, Priority: P2, Session: &Session{Activity: "waiting-for-human"}},
			want: IssueDisplayActive,
		},
		{
			name: "waiting for tool review handoff remains active",
			task: Task{Status: StatusInReview, Priority: P2, Session: &Session{Activity: "waiting-for-tool"}},
			want: IssueDisplayActive,
		},
		{
			name: "busy review handoff remains active",
			task: Task{Status: StatusInReview, Priority: P2, Session: &Session{Activity: "working"}},
			want: IssueDisplayActive,
		},
		{
			name: "no-agent review handoff is review",
			task: Task{Status: StatusInReview, Priority: P2, Session: &Session{Activity: "no-agent"}},
			want: IssueDisplayReview,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.task.IssueDisplayPhase(); got != tt.want {
				t.Fatalf("IssueDisplayPhase() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestNewIssueStateRejectsInvalidCombinations(t *testing.T) {
	tests := []struct {
		name  string
		input IssueStateParts
	}{
		{
			name: "review cannot exist on open issue",
			input: IssueStateParts{
				Workflow: IssueWorkflowOpen,
				Review:   IssueReviewRequested,
			},
		},
		{
			name: "non closed issue cannot have close outcome",
			input: IssueStateParts{
				Workflow:     IssueWorkflowActive,
				CloseOutcome: IssueCloseCompleted,
			},
		},
		{
			name: "closed issue requires close outcome",
			input: IssueStateParts{
				Workflow: IssueWorkflowClosed,
			},
		},
		{
			name: "closed issue cannot remain in review",
			input: IssueStateParts{
				Workflow:     IssueWorkflowClosed,
				Review:       IssueReviewRequested,
				CloseOutcome: IssueCloseCompleted,
			},
		},
		{
			name: "unknown archive state is rejected",
			input: IssueStateParts{
				Workflow: IssueWorkflowOpen,
				Archive:  IssueArchiveState("maybe"),
			},
		},
		{
			name: "unknown deletion state is rejected",
			input: IssueStateParts{
				Workflow: IssueWorkflowOpen,
				Deletion: IssueDeletionState("missing"),
			},
		},
		{
			name: "missing workflow is rejected",
			input: IssueStateParts{
				Review: IssueReviewNone,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewIssueState(tt.input); err == nil {
				t.Fatal("NewIssueState() error = nil, want invalid combination error")
			}
		})
	}
}

func TestIssueStateTransitions(t *testing.T) {
	backlog := mustIssueState(t, IssueStateParts{Workflow: IssueWorkflowBacklog})
	open := mustIssueState(t, IssueStateParts{Workflow: IssueWorkflowOpen})
	active := mustIssueState(t, IssueStateParts{Workflow: IssueWorkflowActive})
	review := mustIssueState(t, IssueStateParts{Workflow: IssueWorkflowActive, Review: IssueReviewRequested})
	completed := mustIssueState(t, IssueStateParts{Workflow: IssueWorkflowClosed, CloseOutcome: IssueCloseCompleted})
	cancelled := mustIssueState(t, IssueStateParts{Workflow: IssueWorkflowClosed, CloseOutcome: IssueCloseCancelled})
	archivedOpen := mustIssueState(t, IssueStateParts{Workflow: IssueWorkflowOpen, Archive: IssueArchiveArchived})
	tombstonedOpen := mustIssueState(t, IssueStateParts{Workflow: IssueWorkflowOpen, Deletion: IssueDeletionTombstoned})

	valid := []struct {
		name string
		from IssueState
		to   IssueState
	}{
		{"backlog to open", backlog, open},
		{"open to backlog", open, backlog},
		{"open to active", open, active},
		{"active to review", active, review},
		{"review to completed", review, completed},
		{"active to cancelled", active, cancelled},
		{"completed can reopen", completed, open},
		{"cancelled can reopen", cancelled, backlog},
		{"archived no-op is valid", archivedOpen, archivedOpen},
		{"tombstoned no-op is valid", tombstonedOpen, tombstonedOpen},
	}

	for _, tt := range valid {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateIssueStateTransition(tt.from, tt.to); err != nil {
				t.Fatalf("ValidateIssueStateTransition() error = %v", err)
			}
		})
	}

	invalid := []struct {
		name string
		from IssueState
		to   IssueState
	}{
		{"backlog cannot skip straight to review", backlog, review},
		{"completed cannot become cancelled", completed, cancelled},
		{"cancelled cannot become completed", cancelled, completed},
		{"backlog cannot complete without becoming active", backlog, completed},
		{"cancelled cannot go straight to review", cancelled, review},
		{"archived issue cannot change workflow", archivedOpen, active},
		{"zero state cannot transition", IssueState{}, open},
	}

	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateIssueStateTransition(tt.from, tt.to); err == nil {
				t.Fatal("ValidateIssueStateTransition() error = nil, want invalid transition")
			}
		})
	}
}

func mustIssueState(t *testing.T, parts IssueStateParts) IssueState {
	t.Helper()
	state, err := NewIssueState(parts)
	if err != nil {
		t.Fatalf("NewIssueState(%+v) error = %v", parts, err)
	}
	return state
}
