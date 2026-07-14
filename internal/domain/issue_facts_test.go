package domain

import (
	"slices"
	"strings"
	"testing"
)

func TestIssueFactsDeriveReviewReadyVisibilityAndReasons(t *testing.T) {
	reviewState := mustIssueState(t, IssueStateParts{
		Workflow: IssueWorkflowActive,
		Review:   IssueReviewRequested,
	})
	tests := []struct {
		name              string
		session           *Session
		wantPhase         IssueDisplayPhase
		wantReviewVisible bool
		wantHuman         bool
		wantAI            bool
		wantDelegated     bool
		wantReason        string
	}{
		{
			name:              "idle review handoff is review-ready",
			session:           &Session{Activity: string(SessionIdle), ActivitySource: "hooks"},
			wantPhase:         IssueDisplayReview,
			wantReviewVisible: true,
			wantReason:        "review-ready card is visible",
		},
		{
			name:       "waiting human review handoff stays active",
			session:    &Session{Activity: "waiting-for-human", ActivitySource: "agent"},
			wantPhase:  IssueDisplayActive,
			wantHuman:  true,
			wantReason: "review-ready card is still active",
		},
		{
			name:       "waiting AI review handoff stays active",
			session:    &Session{Activity: "waiting_ai", ActivitySource: "agent"},
			wantPhase:  IssueDisplayActive,
			wantAI:     true,
			wantReason: "review-ready card is still active",
		},
		{
			name:          "waiting tool review handoff marks delegated operation",
			session:       &Session{Activity: "waiting-for-tool", ActivitySource: "agent"},
			wantPhase:     IssueDisplayActive,
			wantDelegated: true,
			wantReason:    "review-ready card is still active",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			facts := DeriveIssueFacts(IssueFactsInput{
				Status:         StatusInReview,
				State:          reviewState,
				Priority:       P2,
				Session:        tt.session,
				HasTmuxSession: true,
			})
			if facts.DisplayPhase != tt.wantPhase {
				t.Fatalf("DisplayPhase = %s, want %s; reasons=%v", facts.DisplayPhase, tt.wantPhase, facts.ReasonMessages())
			}
			if facts.ReviewReadyVisible != tt.wantReviewVisible {
				t.Fatalf("ReviewReadyVisible = %t, want %t", facts.ReviewReadyVisible, tt.wantReviewVisible)
			}
			if facts.WaitingHuman != tt.wantHuman {
				t.Fatalf("WaitingHuman = %t, want %t", facts.WaitingHuman, tt.wantHuman)
			}
			if facts.WaitingAI != tt.wantAI {
				t.Fatalf("WaitingAI = %t, want %t", facts.WaitingAI, tt.wantAI)
			}
			if facts.DelegatedOperation != tt.wantDelegated {
				t.Fatalf("DelegatedOperation = %t, want %t", facts.DelegatedOperation, tt.wantDelegated)
			}
			if !issueFactReasonsContain(facts, tt.wantReason) {
				t.Fatalf("reasons = %#v, want substring %q", facts.ReasonMessages(), tt.wantReason)
			}
		})
	}
}

func TestHumanAttentionRank(t *testing.T) {
	tests := []struct {
		name string
		task Task
		want HumanAttentionTier
	}{
		{name: "ordinary work", task: Task{Status: StatusInProgress}, want: HumanAttentionNone},
		{name: "review ready", task: Task{Status: StatusInReview}, want: HumanAttentionReviewReady},
		{name: "waiting human", task: Task{Status: StatusInProgress, Session: &Session{Activity: "waiting-for-human"}}, want: HumanAttentionWaitingHuman},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HumanAttentionRank(tt.task); got != tt.want {
				t.Fatalf("HumanAttentionRank() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestIssueFactsProjectOnlyHumanFindingsAsHumanAuthority(t *testing.T) {
	tests := []struct {
		name       string
		acceptance InvestigationAcceptance
		wantHuman  bool
	}{
		{name: "unaccepted human findings", acceptance: InvestigationAcceptance{Disposition: InvestigationDispositionHumanFindings, Reason: "explicit acceptance required"}, wantHuman: true},
		{name: "accepted human findings", acceptance: InvestigationAcceptance{Disposition: InvestigationDispositionHumanFindings, Accepted: true}},
		{name: "unaccepted internal review", acceptance: InvestigationAcceptance{Disposition: InvestigationDispositionInternalReview, Reason: "review outcome required"}},
		{name: "accepted internal review", acceptance: InvestigationAcceptance{Disposition: InvestigationDispositionInternalReview, Accepted: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			facts := DeriveIssueFacts(IssueFactsInput{Status: StatusInReview, Type: TypeInvestigation, InvestigationAcceptance: &tt.acceptance})
			if facts.WaitingHuman != tt.wantHuman {
				t.Fatalf("WaitingHuman = %t, want %t: %+v", facts.WaitingHuman, tt.wantHuman, facts)
			}
			if tt.wantHuman && (facts.WaitingHumanSource != WaitingHumanSourceInvestigationAcceptance || facts.WaitingHumanReason != tt.acceptance.Reason) {
				t.Fatalf("human authority facts = %+v", facts)
			}
		})
	}
}

func TestIssueFactsExposeClosedArchiveTombstoneAndOperationBlockers(t *testing.T) {
	closed := mustIssueState(t, IssueStateParts{
		Workflow:     IssueWorkflowClosed,
		CloseOutcome: IssueCloseCancelled,
		Archive:      IssueArchiveArchived,
	})
	facts := DeriveIssueFacts(IssueFactsInput{
		Status: StatusDone,
		State:  closed,
		OperationBlockers: []IssueOperationBlocker{{
			OperationID:          "op-1",
			Kind:                 "task.close",
			State:                "queued",
			BlockedResourceKeys:  []string{"issue:az-1"},
			BlockingOperationIDs: []string{"op-running"},
		}},
	})

	if facts.DisplayPhase != IssueDisplayCancelled || facts.ClosedOutcome != IssueCloseCancelled {
		t.Fatalf("facts = %+v, want cancelled display with cancelled outcome", facts)
	}
	if facts.ArchiveState != IssueArchiveArchived {
		t.Fatalf("archive = %s, want archived", facts.ArchiveState)
	}
	if !facts.DelegatedOperation || len(facts.OperationBlockers) != 1 {
		t.Fatalf("operation blockers = %+v, delegated=%t", facts.OperationBlockers, facts.DelegatedOperation)
	}
	facts.OperationBlockers[0].BlockedResourceKeys[0] = "mutated"
	if got := DeriveIssueFacts(IssueFactsInput{
		Status: StatusDone,
		State:  closed,
		OperationBlockers: []IssueOperationBlocker{{
			BlockedResourceKeys: []string{"issue:az-1"},
		}},
	}).OperationBlockers[0].BlockedResourceKeys[0]; got != "issue:az-1" {
		t.Fatalf("operation blocker resources were not cloned, got %q", got)
	}
	for _, want := range []string{"closed outcome is cancelled", "issue is archived"} {
		if !issueFactReasonsContain(facts, want) {
			t.Fatalf("reasons = %#v, want %q", facts.ReasonMessages(), want)
		}
	}
}

func TestTaskIssueFactsPrefersPrecomputedProjection(t *testing.T) {
	task := Task{
		Status: StatusOpen,
		Facts: IssueFacts{
			DisplayPhase:  IssueDisplayReview,
			DisplayStatus: StatusInReview,
			Reasons:       []IssueFactReason{{Code: "test", Message: "precomputed projection"}},
		},
	}

	facts := task.IssueFacts()
	if facts.DisplayPhase != IssueDisplayReview || facts.DisplayStatus != StatusInReview {
		t.Fatalf("IssueFacts() = %+v, want precomputed review facts", facts)
	}
	if !slices.Contains(facts.ReasonMessages(), "precomputed projection") {
		t.Fatalf("reasons = %#v, want precomputed reason", facts.ReasonMessages())
	}
}

func TestTaskIssueFactsIgnoresPartialPrecomputedProjection(t *testing.T) {
	task := Task{
		Status: StatusInReview,
		State: mustIssueState(t, IssueStateParts{
			Workflow: IssueWorkflowActive,
			Review:   IssueReviewRequested,
		}),
		Session: &Session{Activity: string(SessionIdle)},
		Facts: IssueFacts{
			LifecycleState: IssueWorkflowActive,
		},
	}

	facts := task.IssueFacts()
	if facts.DisplayPhase != IssueDisplayReview || !facts.ReviewReadyVisible {
		t.Fatalf("IssueFacts() = %+v, want derived review-ready facts", facts)
	}
}

func issueFactReasonsContain(facts IssueFacts, want string) bool {
	for _, reason := range facts.ReasonMessages() {
		if strings.Contains(reason, want) {
			return true
		}
	}
	return false
}
