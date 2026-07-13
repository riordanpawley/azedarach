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

// IssueDisposition is durable lifecycle authority. "Open" is deliberately not
// represented here: it is the presentation phase of ready + idle.
type IssueDisposition string

const (
	IssueDispositionBacklog   IssueDisposition = "backlog"
	IssueDispositionReady     IssueDisposition = "ready"
	IssueDispositionCompleted IssueDisposition = "completed"
	IssueDispositionCancelled IssueDisposition = "cancelled"
)

// IssueEngagement records whether ready work is idle, being worked, or has
// requested review. Non-ready dispositions are always idle.
type IssueEngagement string

const (
	IssueEngagementIdle            IssueEngagement = "idle"
	IssueEngagementWorking         IssueEngagement = "working"
	IssueEngagementReviewRequested IssueEngagement = "review_requested"
)

type IssueVisibility string

const (
	IssueVisibilityLive     IssueVisibility = "live"
	IssueVisibilityArchived IssueVisibility = "archived"
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
}

type IssueState struct {
	Disposition IssueDisposition `json:"disposition" msgpack:"disposition"`
	Engagement  IssueEngagement  `json:"engagement" msgpack:"engagement"`
	Visibility  IssueVisibility  `json:"visibility" msgpack:"visibility"`
}

type CanonicalIssueStateParts struct {
	Disposition IssueDisposition
	Engagement  IssueEngagement
	Visibility  IssueVisibility
}

type LegacyIssueStateInput struct {
	Status    Status
	Priority  Priority
	Archived  bool
	Cancelled bool
}

func IssueStateFromLegacy(input LegacyIssueStateInput) (IssueState, error) {
	parts := IssueStateParts{
		Workflow:     IssueWorkflowOpen,
		Review:       IssueReviewNone,
		CloseOutcome: IssueCloseNone,
		Archive:      archiveStateFromBool(input.Archived),
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

func IssueStateFromStatus(status Status) (IssueState, error) {
	parts := IssueStateParts{
		Workflow:     IssueWorkflowOpen,
		Review:       IssueReviewNone,
		CloseOutcome: IssueCloseNone,
		Archive:      IssueArchiveLive,
	}
	switch normalizeStatus(status) {
	case StatusOpen:
		parts.Workflow = IssueWorkflowOpen
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
		return IssueState{}, fmt.Errorf("invalid issue status: %s", status)
	}
	return NewIssueState(parts)
}

func NewIssueState(parts IssueStateParts) (IssueState, error) {
	parts = normalizeIssueStateParts(parts)
	if err := validateIssueStateParts(parts); err != nil {
		return IssueState{}, err
	}
	canonical := CanonicalIssueStateParts{Visibility: IssueVisibilityLive, Engagement: IssueEngagementIdle}
	if parts.Archive == IssueArchiveArchived {
		canonical.Visibility = IssueVisibilityArchived
	}
	switch parts.Workflow {
	case IssueWorkflowBacklog:
		canonical.Disposition = IssueDispositionBacklog
	case IssueWorkflowOpen:
		canonical.Disposition = IssueDispositionReady
	case IssueWorkflowActive:
		canonical.Disposition = IssueDispositionReady
		canonical.Engagement = IssueEngagementWorking
	case IssueWorkflowClosed:
		if parts.CloseOutcome == IssueCloseCancelled {
			canonical.Disposition = IssueDispositionCancelled
		} else {
			canonical.Disposition = IssueDispositionCompleted
		}
	}
	if parts.Review == IssueReviewRequested {
		canonical.Engagement = IssueEngagementReviewRequested
	}
	return NewCanonicalIssueState(canonical)
}

func NewCanonicalIssueState(parts CanonicalIssueStateParts) (IssueState, error) {
	if parts.Engagement == "" {
		parts.Engagement = IssueEngagementIdle
	}
	if parts.Visibility == "" {
		parts.Visibility = IssueVisibilityLive
	}
	s := IssueState{Disposition: parts.Disposition, Engagement: parts.Engagement, Visibility: parts.Visibility}
	if err := s.Validate(); err != nil {
		return IssueState{}, err
	}
	return s, nil
}

func (s IssueState) Workflow() IssueWorkflow {
	switch s.Disposition {
	case IssueDispositionBacklog:
		return IssueWorkflowBacklog
	case IssueDispositionReady:
		if s.Engagement == IssueEngagementIdle {
			return IssueWorkflowOpen
		}
		return IssueWorkflowActive
	default:
		return IssueWorkflowClosed
	}
}

func (s IssueState) Review() IssueReviewState {
	if s.Engagement == IssueEngagementReviewRequested {
		return IssueReviewRequested
	}
	return IssueReviewNone
}

func (s IssueState) CloseOutcome() IssueCloseOutcome {
	switch s.Disposition {
	case IssueDispositionCompleted:
		return IssueCloseCompleted
	case IssueDispositionCancelled:
		return IssueCloseCancelled
	default:
		return IssueCloseNone
	}
}

func (s IssueState) Archive() IssueArchiveState {
	if s.Visibility == IssueVisibilityArchived {
		return IssueArchiveArchived
	}
	return IssueArchiveLive
}

func (s IssueState) BoardPhase() IssueBoardPhase {
	if err := s.Validate(); err != nil {
		return IssueBoardUnknown
	}
	if s.IsClosed() {
		return IssueBoardClosed
	}
	if s.Review() == IssueReviewRequested {
		return IssueBoardReview
	}
	switch s.Workflow() {
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
	if s.IsClosed() {
		if s.CloseOutcome() == IssueCloseCancelled {
			return IssueDisplayCancelled
		}
		return IssueDisplayDone
	}
	if s.Review() == IssueReviewRequested {
		return IssueDisplayReview
	}
	switch s.Workflow() {
	case IssueWorkflowBacklog:
		return IssueDisplayBacklog
	case IssueWorkflowActive:
		return IssueDisplayActive
	default:
		return IssueDisplayOpen
	}
}

func (s IssueState) IsArchived() bool {
	return s.Visibility == IssueVisibilityArchived
}

func (s IssueState) IsClosed() bool {
	return s.Validate() == nil && (s.Disposition == IssueDispositionCompleted || s.Disposition == IssueDispositionCancelled)
}

func (s IssueState) IsZero() bool {
	return s == IssueState{}
}

func (s IssueState) Validate() error {
	switch s.Disposition {
	case IssueDispositionBacklog, IssueDispositionReady, IssueDispositionCompleted, IssueDispositionCancelled:
	default:
		return fmt.Errorf("invalid issue disposition: %s", s.Disposition)
	}
	switch s.Engagement {
	case IssueEngagementIdle, IssueEngagementWorking, IssueEngagementReviewRequested:
	default:
		return fmt.Errorf("invalid issue engagement: %s", s.Engagement)
	}
	switch s.Visibility {
	case IssueVisibilityLive, IssueVisibilityArchived:
	default:
		return fmt.Errorf("invalid issue visibility: %s", s.Visibility)
	}
	if s.Disposition != IssueDispositionReady && s.Engagement != IssueEngagementIdle {
		return fmt.Errorf("issue disposition %s requires idle engagement, got %s", s.Disposition, s.Engagement)
	}
	if s.Visibility == IssueVisibilityArchived && s.Engagement != IssueEngagementIdle {
		return fmt.Errorf("archived issue requires idle engagement, got %s", s.Engagement)
	}
	return nil
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
	if s.IsClosed() {
		if s.CloseOutcome() == IssueCloseCancelled {
			return issueTransitionCancel
		}
		return issueTransitionComplete
	}
	if s.Review() == IssueReviewRequested {
		return issueTransitionReview
	}
	switch s.Workflow() {
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
	if !t.State.IsZero() {
		if err := t.State.Validate(); err != nil {
			return IssueState{}, err
		}
		return t.State, nil
	}
	return IssueStateFromStatus(t.Status)
}

func (t Task) IssueClosed() bool {
	state, err := t.IssueState()
	return err == nil && state.IsClosed()
}

func (t Task) IssueDisplayPhase() IssueDisplayPhase {
	return t.IssueFacts().DisplayPhase
}

func (t Task) IssueDisplayStatusText() string {
	return t.IssueDisplayPhase().StatusText()
}

func (t Task) IssueDisplayFilterStatus() Status {
	return t.IssueFacts().DisplayStatus
}

func IssueDisplayPhasesForTasks(tasks []Task) []IssueDisplayPhase {
	hasBacklog := false
	hasCancelled := false
	for _, task := range tasks {
		switch task.IssueFacts().DisplayPhase {
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
