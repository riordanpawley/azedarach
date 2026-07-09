package domain

import (
	"fmt"
	"strings"
)

// StatusCancelled is a legacy/v2 bridge value. The current store primarily
// persists completed issues as "closed"; the v2 mapper also accepts explicit
// cancelled status input from future callers or external projections.
const StatusCancelled Status = "cancelled"

type IssueWorkflow string

const (
	IssueWorkflowBacklog IssueWorkflow = "backlog"
	IssueWorkflowOpen    IssueWorkflow = "open"
	IssueWorkflowActive  IssueWorkflow = "active"
	IssueWorkflowClosed  IssueWorkflow = "closed"
)

type IssueReviewState string

const (
	IssueReviewNone      IssueReviewState = "none"
	IssueReviewRequested IssueReviewState = "requested"
)

type IssueCloseOutcome string

const (
	IssueCloseNone      IssueCloseOutcome = "none"
	IssueCloseCompleted IssueCloseOutcome = "completed"
	IssueCloseCancelled IssueCloseOutcome = "cancelled"
)

type IssueArchiveState string

const (
	IssueArchiveLive     IssueArchiveState = "live"
	IssueArchiveArchived IssueArchiveState = "archived"
)

type IssueDeletionState string

const (
	IssueDeletionPresent    IssueDeletionState = "present"
	IssueDeletionTombstoned IssueDeletionState = "tombstoned"
)

type IssueBoardPhase string

const (
	IssueBoardUnknown IssueBoardPhase = "unknown"
	IssueBoardBacklog IssueBoardPhase = "backlog"
	IssueBoardOpen    IssueBoardPhase = "open"
	IssueBoardActive  IssueBoardPhase = "active"
	IssueBoardReview  IssueBoardPhase = "review"
	IssueBoardClosed  IssueBoardPhase = "closed"
)

type IssueDisplayPhase string

const (
	IssueDisplayUnknown   IssueDisplayPhase = "unknown"
	IssueDisplayBacklog   IssueDisplayPhase = "backlog"
	IssueDisplayOpen      IssueDisplayPhase = "open"
	IssueDisplayActive    IssueDisplayPhase = "active"
	IssueDisplayReview    IssueDisplayPhase = "review"
	IssueDisplayDone      IssueDisplayPhase = "done"
	IssueDisplayCancelled IssueDisplayPhase = "cancelled"
)

type IssueStateParts struct {
	Workflow     IssueWorkflow
	Review       IssueReviewState
	CloseOutcome IssueCloseOutcome
	Archive      IssueArchiveState
	Deletion     IssueDeletionState
}

type IssueState struct {
	workflow     IssueWorkflow
	review       IssueReviewState
	closeOutcome IssueCloseOutcome
	archive      IssueArchiveState
	deletion     IssueDeletionState
}

type LegacyIssueStateInput struct {
	Status     Status
	Priority   Priority
	Archived   bool
	Tombstoned bool
	Cancelled  bool
}

func IssueStateFromLegacy(input LegacyIssueStateInput) (IssueState, error) {
	parts := IssueStateParts{
		Workflow:     IssueWorkflowOpen,
		Review:       IssueReviewNone,
		CloseOutcome: IssueCloseNone,
		Archive:      archiveStateFromBool(input.Archived),
		Deletion:     deletionStateFromBool(input.Tombstoned),
	}

	switch normalizeStatus(input.Status) {
	case StatusOpen:
		if input.Priority == P4 {
			parts.Workflow = IssueWorkflowBacklog
		} else {
			parts.Workflow = IssueWorkflowOpen
		}
	case StatusInProgress:
		parts.Workflow = IssueWorkflowActive
	case StatusInReview:
		parts.Workflow = IssueWorkflowActive
		parts.Review = IssueReviewRequested
	case StatusDone:
		parts.Workflow = IssueWorkflowClosed
		parts.CloseOutcome = IssueCloseCompleted
	case StatusCancelled:
		parts.Workflow = IssueWorkflowClosed
		parts.CloseOutcome = IssueCloseCancelled
	default:
		return IssueState{}, fmt.Errorf("invalid legacy issue status: %s", input.Status)
	}

	if input.Cancelled {
		if parts.Workflow != IssueWorkflowClosed {
			return IssueState{}, fmt.Errorf("cancelled outcome requires closed workflow: %s", parts.Workflow)
		}
		parts.CloseOutcome = IssueCloseCancelled
	}

	return NewIssueState(parts)
}

func NewIssueState(parts IssueStateParts) (IssueState, error) {
	parts = normalizeIssueStateParts(parts)
	if err := validateIssueStateParts(parts); err != nil {
		return IssueState{}, err
	}
	return IssueState{
		workflow:     parts.Workflow,
		review:       parts.Review,
		closeOutcome: parts.CloseOutcome,
		archive:      parts.Archive,
		deletion:     parts.Deletion,
	}, nil
}

func (s IssueState) Workflow() IssueWorkflow {
	return s.workflow
}

func (s IssueState) Review() IssueReviewState {
	return s.review
}

func (s IssueState) CloseOutcome() IssueCloseOutcome {
	return s.closeOutcome
}

func (s IssueState) Archive() IssueArchiveState {
	return s.archive
}

func (s IssueState) Deletion() IssueDeletionState {
	return s.deletion
}

func (s IssueState) BoardPhase() IssueBoardPhase {
	if err := s.Validate(); err != nil {
		return IssueBoardUnknown
	}
	if s.workflow == IssueWorkflowClosed {
		return IssueBoardClosed
	}
	if s.review == IssueReviewRequested {
		return IssueBoardReview
	}
	switch s.workflow {
	case IssueWorkflowBacklog:
		return IssueBoardBacklog
	case IssueWorkflowActive:
		return IssueBoardActive
	default:
		return IssueBoardOpen
	}
}

func (s IssueState) DisplayPhase() IssueDisplayPhase {
	if err := s.Validate(); err != nil {
		return IssueDisplayUnknown
	}
	if s.workflow == IssueWorkflowClosed {
		if s.closeOutcome == IssueCloseCancelled {
			return IssueDisplayCancelled
		}
		return IssueDisplayDone
	}
	if s.review == IssueReviewRequested {
		return IssueDisplayReview
	}
	switch s.workflow {
	case IssueWorkflowBacklog:
		return IssueDisplayBacklog
	case IssueWorkflowActive:
		return IssueDisplayActive
	default:
		return IssueDisplayOpen
	}
}

func (s IssueState) IsArchived() bool {
	return s.archive == IssueArchiveArchived
}

func (s IssueState) IsTombstoned() bool {
	return s.deletion == IssueDeletionTombstoned
}

func (s IssueState) Validate() error {
	return validateIssueStateParts(IssueStateParts{
		Workflow:     s.workflow,
		Review:       s.review,
		CloseOutcome: s.closeOutcome,
		Archive:      s.archive,
		Deletion:     s.deletion,
	})
}

func ValidateIssueStateTransition(from, to IssueState) error {
	if err := from.Validate(); err != nil {
		return fmt.Errorf("invalid source issue state: %w", err)
	}
	if err := to.Validate(); err != nil {
		return fmt.Errorf("invalid target issue state: %w", err)
	}
	if from == to {
		return nil
	}
	if from.IsArchived() || to.IsArchived() {
		return fmt.Errorf("workflow transition cannot change archived issue state")
	}
	if from.IsTombstoned() || to.IsTombstoned() {
		return fmt.Errorf("workflow transition cannot change tombstoned issue state")
	}
	if issueStateTransitionAllowed(from, to) {
		return nil
	}
	return fmt.Errorf("invalid issue state transition from %s to %s", from.transitionKey(), to.transitionKey())
}

func normalizeIssueStateParts(parts IssueStateParts) IssueStateParts {
	if parts.Review == "" {
		parts.Review = IssueReviewNone
	}
	if parts.CloseOutcome == "" {
		parts.CloseOutcome = IssueCloseNone
	}
	if parts.Archive == "" {
		parts.Archive = IssueArchiveLive
	}
	if parts.Deletion == "" {
		parts.Deletion = IssueDeletionPresent
	}
	return parts
}

func validateIssueStateParts(parts IssueStateParts) error {
	switch parts.Workflow {
	case IssueWorkflowBacklog, IssueWorkflowOpen, IssueWorkflowActive, IssueWorkflowClosed:
	default:
		return fmt.Errorf("invalid issue workflow: %s", parts.Workflow)
	}
	switch parts.Review {
	case IssueReviewNone, IssueReviewRequested:
	default:
		return fmt.Errorf("invalid issue review state: %s", parts.Review)
	}
	switch parts.CloseOutcome {
	case IssueCloseNone, IssueCloseCompleted, IssueCloseCancelled:
	default:
		return fmt.Errorf("invalid issue close outcome: %s", parts.CloseOutcome)
	}
	switch parts.Archive {
	case IssueArchiveLive, IssueArchiveArchived:
	default:
		return fmt.Errorf("invalid issue archive state: %s", parts.Archive)
	}
	switch parts.Deletion {
	case IssueDeletionPresent, IssueDeletionTombstoned:
	default:
		return fmt.Errorf("invalid issue deletion state: %s", parts.Deletion)
	}
	if parts.Review == IssueReviewRequested && parts.Workflow != IssueWorkflowActive {
		return fmt.Errorf("review state %s requires active workflow, got %s", parts.Review, parts.Workflow)
	}
	if parts.Workflow == IssueWorkflowClosed {
		if parts.CloseOutcome == IssueCloseNone {
			return fmt.Errorf("closed workflow requires close outcome")
		}
		if parts.Review != IssueReviewNone {
			return fmt.Errorf("closed workflow cannot have review state %s", parts.Review)
		}
		return nil
	}
	if parts.CloseOutcome != IssueCloseNone {
		return fmt.Errorf("non-closed workflow %s cannot have close outcome %s", parts.Workflow, parts.CloseOutcome)
	}
	return nil
}

type issueStateTransition string

const (
	issueTransitionBacklog  issueStateTransition = "backlog"
	issueTransitionOpen     issueStateTransition = "open"
	issueTransitionActive   issueStateTransition = "active"
	issueTransitionReview   issueStateTransition = "review"
	issueTransitionComplete issueStateTransition = "completed"
	issueTransitionCancel   issueStateTransition = "cancelled"
)

func (s IssueState) transitionKey() issueStateTransition {
	if s.workflow == IssueWorkflowClosed {
		if s.closeOutcome == IssueCloseCancelled {
			return issueTransitionCancel
		}
		return issueTransitionComplete
	}
	if s.review == IssueReviewRequested {
		return issueTransitionReview
	}
	switch s.workflow {
	case IssueWorkflowBacklog:
		return issueTransitionBacklog
	case IssueWorkflowActive:
		return issueTransitionActive
	default:
		return issueTransitionOpen
	}
}

func issueStateTransitionAllowed(from, to IssueState) bool {
	allowed := map[issueStateTransition]map[issueStateTransition]bool{
		issueTransitionBacklog: {
			issueTransitionOpen:   true,
			issueTransitionActive: true,
			issueTransitionCancel: true,
		},
		issueTransitionOpen: {
			issueTransitionBacklog:  true,
			issueTransitionActive:   true,
			issueTransitionReview:   true,
			issueTransitionComplete: true,
			issueTransitionCancel:   true,
		},
		issueTransitionActive: {
			issueTransitionBacklog:  true,
			issueTransitionOpen:     true,
			issueTransitionReview:   true,
			issueTransitionComplete: true,
			issueTransitionCancel:   true,
		},
		issueTransitionReview: {
			issueTransitionOpen:     true,
			issueTransitionActive:   true,
			issueTransitionComplete: true,
			issueTransitionCancel:   true,
		},
		issueTransitionComplete: {
			issueTransitionBacklog: true,
			issueTransitionOpen:    true,
			issueTransitionActive:  true,
			issueTransitionReview:  true,
		},
		issueTransitionCancel: {
			issueTransitionBacklog: true,
			issueTransitionOpen:    true,
		},
	}
	return allowed[from.transitionKey()][to.transitionKey()]
}

func normalizeStatus(status Status) Status {
	return Status(strings.ToLower(strings.TrimSpace(string(status))))
}

func archiveStateFromBool(archived bool) IssueArchiveState {
	if archived {
		return IssueArchiveArchived
	}
	return IssueArchiveLive
}

func deletionStateFromBool(tombstoned bool) IssueDeletionState {
	if tombstoned {
		return IssueDeletionTombstoned
	}
	return IssueDeletionPresent
}

func (p IssueDisplayPhase) Label() string {
	switch p {
	case IssueDisplayBacklog:
		return "Backlog"
	case IssueDisplayOpen:
		return "Open"
	case IssueDisplayActive:
		return "In Progress"
	case IssueDisplayReview:
		return "In Review"
	case IssueDisplayDone:
		return "Done"
	case IssueDisplayCancelled:
		return "Cancelled"
	default:
		return "Unknown"
	}
}

func (p IssueDisplayPhase) StatusText() string {
	switch p {
	case IssueDisplayBacklog:
		return "backlog"
	case IssueDisplayOpen:
		return string(StatusOpen)
	case IssueDisplayActive:
		return string(StatusInProgress)
	case IssueDisplayReview:
		return string(StatusInReview)
	case IssueDisplayDone:
		return string(StatusDone)
	case IssueDisplayCancelled:
		return string(StatusCancelled)
	default:
		return "unknown"
	}
}

func (t Task) IssueState() (IssueState, error) {
	return IssueStateFromLegacy(LegacyIssueStateInput{
		Status:   t.Status,
		Priority: t.Priority,
	})
}

func (t Task) IssueDisplayPhase() IssueDisplayPhase {
	state, err := t.IssueState()
	if err != nil {
		return IssueDisplayUnknown
	}
	phase := state.DisplayPhase()
	if phase == IssueDisplayReview && taskHasActiveReviewBlockingSession(t) {
		return IssueDisplayActive
	}
	return phase
}

func (t Task) IssueDisplayStatusText() string {
	return t.IssueDisplayPhase().StatusText()
}

func (t Task) IssueDisplayFilterStatus() Status {
	return t.IssueDisplayPhase().FilterStatus()
}

func IssueDisplayPhasesForTasks(tasks []Task) []IssueDisplayPhase {
	hasBacklog := false
	hasCancelled := false
	for _, task := range tasks {
		switch task.IssueDisplayPhase() {
		case IssueDisplayBacklog:
			hasBacklog = true
		case IssueDisplayCancelled:
			hasCancelled = true
		}
	}
	phases := make([]IssueDisplayPhase, 0, 6)
	if hasBacklog {
		phases = append(phases, IssueDisplayBacklog)
	}
	phases = append(phases, IssueDisplayOpen, IssueDisplayActive, IssueDisplayReview, IssueDisplayDone)
	if hasCancelled {
		phases = append(phases, IssueDisplayCancelled)
	}
	return phases
}

func (p IssueDisplayPhase) FilterStatus() Status {
	switch p {
	case IssueDisplayOpen:
		return StatusOpen
	case IssueDisplayActive:
		return StatusInProgress
	case IssueDisplayReview:
		return StatusInReview
	case IssueDisplayDone:
		return StatusDone
	case IssueDisplayCancelled:
		return StatusCancelled
	default:
		return Status("")
	}
}

func taskHasActiveReviewBlockingSession(task Task) bool {
	if task.Session == nil {
		return false
	}
	rawActivity := strings.ToLower(strings.TrimSpace(task.Session.Activity))
	switch rawActivity {
	case string(SessionBusy), "starting", "working", string(SessionWaiting), "waiting-for-human", "waiting-for-ai", "waiting-for-tool":
		return true
	}
	switch task.Session.DisplayActivity() {
	case string(SessionBusy), "starting", "working", string(SessionWaiting):
		return true
	default:
		return false
	}
}
