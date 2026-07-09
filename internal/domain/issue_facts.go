package domain

import (
	"fmt"
	"strings"
)

type IssueFactReason struct {
	Code    string `json:"code" msgpack:"code"`
	Message string `json:"message" msgpack:"message"`
}

type IssueOperationBlocker struct {
	OperationID          string   `json:"operation_id,omitempty" msgpack:"operation_id,omitempty"`
	Kind                 string   `json:"kind,omitempty" msgpack:"kind,omitempty"`
	State                string   `json:"state,omitempty" msgpack:"state,omitempty"`
	Reason               string   `json:"reason,omitempty" msgpack:"reason,omitempty"`
	BlockedResourceKeys  []string `json:"blocked_resource_keys,omitempty" msgpack:"blocked_resource_keys,omitempty"`
	BlockingOperationIDs []string `json:"blocking_operation_ids,omitempty" msgpack:"blocking_operation_ids,omitempty"`
}

type IssueFactsInput struct {
	Status            Status
	Priority          Priority
	State             IssueState
	Session           *Session
	HasTmuxSession    bool
	OperationBlockers []IssueOperationBlocker
}

type IssueFacts struct {
	LifecycleState     IssueWorkflow           `json:"lifecycle_state" msgpack:"lifecycle_state"`
	ReviewState        IssueReviewState        `json:"review_state" msgpack:"review_state"`
	ReviewReadyVisible bool                    `json:"review_ready_visible" msgpack:"review_ready_visible"`
	ClosedOutcome      IssueCloseOutcome       `json:"closed_outcome" msgpack:"closed_outcome"`
	ArchiveState       IssueArchiveState       `json:"archive_state" msgpack:"archive_state"`
	DeletionState      IssueDeletionState      `json:"deletion_state" msgpack:"deletion_state"`
	BoardPhase         IssueBoardPhase         `json:"board_phase" msgpack:"board_phase"`
	DisplayPhase       IssueDisplayPhase       `json:"display_phase" msgpack:"display_phase"`
	DisplayStatus      Status                  `json:"display_status,omitempty" msgpack:"display_status,omitempty"`
	SessionState       SessionState            `json:"session_state,omitempty" msgpack:"session_state,omitempty"`
	SessionActivity    string                  `json:"session_activity,omitempty" msgpack:"session_activity,omitempty"`
	SessionSource      string                  `json:"session_source,omitempty" msgpack:"session_source,omitempty"`
	HasSession         bool                    `json:"has_session" msgpack:"has_session"`
	HasActiveSession   bool                    `json:"has_active_session" msgpack:"has_active_session"`
	WaitingHuman       bool                    `json:"waiting_human" msgpack:"waiting_human"`
	WaitingAI          bool                    `json:"waiting_ai" msgpack:"waiting_ai"`
	DelegatedOperation bool                    `json:"delegated_operation" msgpack:"delegated_operation"`
	OperationBlockers  []IssueOperationBlocker `json:"operation_blockers,omitempty" msgpack:"operation_blockers,omitempty"`
	Reasons            []IssueFactReason       `json:"reasons,omitempty" msgpack:"reasons,omitempty"`
}

func (f IssueFacts) IsZero() bool {
	return f.LifecycleState == "" &&
		f.ReviewState == "" &&
		!f.ReviewReadyVisible &&
		f.ClosedOutcome == "" &&
		f.ArchiveState == "" &&
		f.DeletionState == "" &&
		f.BoardPhase == "" &&
		f.DisplayPhase == "" &&
		f.DisplayStatus == "" &&
		f.SessionState == "" &&
		f.SessionActivity == "" &&
		f.SessionSource == "" &&
		!f.HasSession &&
		!f.HasActiveSession &&
		!f.WaitingHuman &&
		!f.WaitingAI &&
		!f.DelegatedOperation &&
		len(f.OperationBlockers) == 0 &&
		len(f.Reasons) == 0
}

func (f IssueFacts) HasDisplayPredicateFacts() bool {
	return f.DisplayPhase != "" && f.DisplayPhase != IssueDisplayUnknown
}

func (f IssueFacts) MatchesDisplayPhase(phase IssueDisplayPhase) bool {
	return f.DisplayPhase == phase
}

func (f IssueFacts) MatchesFilterStatus(status Status) bool {
	return f.DisplayStatus != "" && f.DisplayStatus == status
}

func (f IssueFacts) ReasonMessages() []string {
	if len(f.Reasons) == 0 {
		return nil
	}
	out := make([]string, 0, len(f.Reasons))
	for _, reason := range f.Reasons {
		msg := strings.TrimSpace(reason.Message)
		if msg == "" {
			continue
		}
		out = append(out, msg)
	}
	return out
}

func DeriveIssueFacts(input IssueFactsInput) IssueFacts {
	state, err := issueStateFromFactsInput(input)
	if err != nil {
		return IssueFacts{
			BoardPhase:    IssueBoardUnknown,
			DisplayPhase:  IssueDisplayUnknown,
			DisplayStatus: Status(""),
			Reasons: []IssueFactReason{{
				Code:    "invalid_issue_state",
				Message: fmt.Sprintf("invalid issue state: %v", err),
			}},
		}
	}

	facts := IssueFacts{
		LifecycleState:     state.Workflow(),
		ReviewState:        state.Review(),
		ClosedOutcome:      state.CloseOutcome(),
		ArchiveState:       state.Archive(),
		DeletionState:      state.Deletion(),
		BoardPhase:         state.BoardPhase(),
		DisplayPhase:       state.DisplayPhase(),
		DisplayStatus:      state.DisplayPhase().FilterStatus(),
		OperationBlockers:  cloneIssueOperationBlockers(input.OperationBlockers),
		DelegatedOperation: len(input.OperationBlockers) > 0,
		Reasons: []IssueFactReason{{
			Code:    "lifecycle",
			Message: fmt.Sprintf("lifecycle is %s", state.Workflow()),
		}},
	}
	if state.Review() == IssueReviewRequested {
		facts.Reasons = append(facts.Reasons, IssueFactReason{
			Code:    "review_requested",
			Message: "review has been requested",
		})
	}
	if state.IsClosed() {
		facts.Reasons = append(facts.Reasons, IssueFactReason{
			Code:    "closed_outcome",
			Message: fmt.Sprintf("closed outcome is %s", state.CloseOutcome()),
		})
	}
	if state.IsArchived() {
		facts.Reasons = append(facts.Reasons, IssueFactReason{
			Code:    "archived",
			Message: "issue is archived",
		})
	}
	if state.IsTombstoned() {
		facts.Reasons = append(facts.Reasons, IssueFactReason{
			Code:    "tombstoned",
			Message: "issue is tombstoned",
		})
	}

	applySessionFacts(&facts, input.Session, input.HasTmuxSession)
	applyReviewReadyFacts(&facts, state, input.Session, input.HasTmuxSession)
	facts.Reasons = append(facts.Reasons, IssueFactReason{
		Code:    "display_phase",
		Message: fmt.Sprintf("card matches %s", facts.DisplayPhase),
	})
	return facts
}

func (t Task) IssueFacts() IssueFacts {
	if !t.Facts.IsZero() && t.Facts.HasDisplayPredicateFacts() {
		return t.Facts
	}
	return DeriveIssueFacts(IssueFactsInput{
		Status:         t.Status,
		Priority:       t.Priority,
		State:          t.State,
		Session:        t.Session,
		HasTmuxSession: t.HasTmuxSession,
	})
}

func IssueFactsForTasks(tasks []Task) map[string]IssueFacts {
	out := make(map[string]IssueFacts, len(tasks))
	for _, task := range tasks {
		out[task.ID.String()] = task.IssueFacts()
	}
	return out
}

func issueStateFromFactsInput(input IssueFactsInput) (IssueState, error) {
	if !input.State.IsZero() {
		if err := input.State.Validate(); err != nil {
			return IssueState{}, err
		}
		return input.State, nil
	}
	return IssueStateFromStatus(input.Status)
}

func applySessionFacts(facts *IssueFacts, session *Session, hasTmuxSession bool) {
	facts.HasSession = session != nil || hasTmuxSession
	if session == nil {
		if hasTmuxSession {
			facts.HasActiveSession = true
			facts.Reasons = append(facts.Reasons, IssueFactReason{
				Code:    "session_present",
				Message: "session runtime is present",
			})
		}
		return
	}
	facts.SessionState = session.State
	facts.SessionActivity = issueFactsSessionActivity(session)
	facts.SessionSource = strings.TrimSpace(session.ActivitySource)
	facts.HasActiveSession = facts.HasSession
	if facts.SessionActivity != "" {
		facts.Reasons = append(facts.Reasons, IssueFactReason{
			Code:    "session_activity",
			Message: fmt.Sprintf("session activity is %s", facts.SessionActivity),
		})
	}
	switch facts.SessionActivity {
	case string(SessionWaiting), "waiting_human", "waiting-for-human":
		facts.WaitingHuman = true
	case "waiting_ai", "waiting-for-ai":
		facts.WaitingAI = true
	case "waiting_tool", "waiting-for-tool":
		facts.DelegatedOperation = true
	}
}

func applyReviewReadyFacts(facts *IssueFacts, state IssueState, session *Session, hasTmuxSession bool) {
	if state.DisplayPhase() != IssueDisplayReview {
		return
	}
	facts.ReviewReadyVisible = session.AllowsReviewReadyPhase(hasTmuxSession)
	if facts.ReviewReadyVisible {
		facts.Reasons = append(facts.Reasons, IssueFactReason{
			Code:    "review_ready_visible",
			Message: "review-ready card is visible because session activity is idle or terminal",
		})
		return
	}
	facts.BoardPhase = IssueBoardActive
	facts.DisplayPhase = IssueDisplayActive
	facts.DisplayStatus = IssueDisplayActive.FilterStatus()
	facts.Reasons = append(facts.Reasons, IssueFactReason{
		Code:    "review_ready_hidden",
		Message: "review-ready card is still active because session activity is busy, waiting, or delegated",
	})
}

func issueFactsSessionActivity(session *Session) string {
	if session == nil {
		return ""
	}
	activity := session.DisplayActivity()
	if activity != "" {
		return activity
	}
	if raw := strings.ToLower(strings.TrimSpace(session.Activity)); raw != "" {
		return raw
	}
	return strings.ToLower(strings.TrimSpace(string(session.State)))
}

func cloneIssueOperationBlockers(blockers []IssueOperationBlocker) []IssueOperationBlocker {
	if len(blockers) == 0 {
		return nil
	}
	out := make([]IssueOperationBlocker, 0, len(blockers))
	for _, blocker := range blockers {
		blocker.BlockedResourceKeys = append([]string(nil), blocker.BlockedResourceKeys...)
		blocker.BlockingOperationIDs = append([]string(nil), blocker.BlockingOperationIDs...)
		out = append(out, blocker)
	}
	return out
}
