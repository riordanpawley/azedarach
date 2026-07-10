package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
)

type OrchestrationScopeKind string

const (
	OrchestrationScopeProject OrchestrationScopeKind = "project"
	OrchestrationScopeRooted  OrchestrationScopeKind = "rooted"
)

func (r OrchestratorWakeReason) Valid() bool {
	switch r {
	case OrchestratorWakeOpenWork, OrchestratorWakeReviewRequest, OrchestratorWakeHumanAnswer, OrchestratorWakeRecovery:
		return true
	default:
		return false
	}
}

// OrchestrationScope is the durable domain selector for one orchestrator.
// Startup flags and environment variables resolve into this value but are not
// themselves authority.
type OrchestrationScope struct {
	Kind        OrchestrationScopeKind `json:"kind"`
	RootIssueID naming.IssueID         `json:"root_issue_id,omitempty"`
}

func ProjectOrchestrationScope() OrchestrationScope {
	return OrchestrationScope{Kind: OrchestrationScopeProject}
}

func RootedOrchestrationScope(rootIssueID string) (OrchestrationScope, error) {
	root, err := naming.ParseIssueID(strings.TrimSpace(rootIssueID))
	if err != nil {
		return OrchestrationScope{}, fmt.Errorf("invalid orchestration root: %w", err)
	}
	return OrchestrationScope{Kind: OrchestrationScopeRooted, RootIssueID: root}, nil
}

// ResolveOrchestrationScope applies startup precedence: an explicit root wins,
// then AZEDARACH_ISSUE_ID, and omission of both selects the whole project.
func ResolveOrchestrationScope(explicitRoot, environmentIssueID string) (OrchestrationScope, error) {
	if root := strings.TrimSpace(explicitRoot); root != "" {
		return RootedOrchestrationScope(root)
	}
	if root := strings.TrimSpace(environmentIssueID); root != "" {
		return RootedOrchestrationScope(root)
	}
	return ProjectOrchestrationScope(), nil
}

type OrchestratorIdentity struct {
	ProjectID string             `json:"project_id"`
	Scope     OrchestrationScope `json:"scope"`
}

func NewOrchestratorIdentity(projectID string, scope OrchestrationScope) (OrchestratorIdentity, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return OrchestratorIdentity{}, fmt.Errorf("project id is required")
	}
	switch scope.Kind {
	case OrchestrationScopeProject:
		if scope.RootIssueID != "" {
			return OrchestratorIdentity{}, fmt.Errorf("project orchestration scope cannot have a root issue")
		}
	case OrchestrationScopeRooted:
		if scope.RootIssueID == "" {
			return OrchestratorIdentity{}, fmt.Errorf("rooted orchestration scope requires a root issue")
		}
	default:
		return OrchestratorIdentity{}, fmt.Errorf("invalid orchestration scope kind %q", scope.Kind)
	}
	return OrchestratorIdentity{ProjectID: projectID, Scope: scope}, nil
}

type OrchestratorLifecycle string

const (
	OrchestratorWorking       OrchestratorLifecycle = "working"
	OrchestratorQuiescent     OrchestratorLifecycle = "quiescent"
	OrchestratorCompleteGrace OrchestratorLifecycle = "complete-grace"
	OrchestratorPaused        OrchestratorLifecycle = "paused"
)

type OrchestratorLifecycleFacts struct {
	OpenIssues             int
	ActiveIssues           int
	ActiveSessions         int
	ReviewRequests         int
	UnresolvedInteractions int
	CompleteSince          *time.Time
}

type OrchestratorLifecyclePolicy struct {
	CompleteGrace time.Duration
	WakeDebounce  time.Duration
}

func DefaultOrchestratorLifecyclePolicy() OrchestratorLifecyclePolicy {
	return OrchestratorLifecyclePolicy{CompleteGrace: 5 * time.Minute, WakeDebounce: 2 * time.Second}
}

func ParseOrchestratorLifecyclePolicy(completeGrace, wakeDebounce string) (OrchestratorLifecyclePolicy, error) {
	defaults := DefaultOrchestratorLifecyclePolicy()
	parse := func(name, value string, fallback time.Duration) (time.Duration, error) {
		value = strings.TrimSpace(value)
		if value == "" {
			return fallback, nil
		}
		duration, err := time.ParseDuration(value)
		if err != nil {
			return 0, fmt.Errorf("parse orchestration %s: %w", name, err)
		}
		if duration < 0 {
			return 0, fmt.Errorf("orchestration %s cannot be negative", name)
		}
		return duration, nil
	}
	grace, err := parse("complete grace", completeGrace, defaults.CompleteGrace)
	if err != nil {
		return OrchestratorLifecyclePolicy{}, err
	}
	debounce, err := parse("wake debounce", wakeDebounce, defaults.WakeDebounce)
	if err != nil {
		return OrchestratorLifecyclePolicy{}, err
	}
	return OrchestratorLifecyclePolicy{CompleteGrace: grace, WakeDebounce: debounce}, nil
}

func (f OrchestratorLifecycleFacts) Complete() bool {
	return f.OpenIssues == 0 && f.ActiveIssues == 0 && f.ActiveSessions == 0 &&
		f.ReviewRequests == 0 && f.UnresolvedInteractions == 0
}

// Quiescent means no executable work is currently active. Human interactions
// may make a project quiescent while still preventing completion.
func (f OrchestratorLifecycleFacts) Quiescent() bool {
	return f.OpenIssues == 0 && f.ActiveIssues == 0 && f.ActiveSessions == 0 && f.ReviewRequests == 0
}

func EvaluateOrchestratorLifecycle(now time.Time, facts OrchestratorLifecycleFacts, policy OrchestratorLifecyclePolicy) OrchestratorLifecycle {
	if !facts.Complete() {
		if facts.Quiescent() {
			return OrchestratorQuiescent
		}
		return OrchestratorWorking
	}
	if facts.CompleteSince == nil || policy.CompleteGrace < 0 || now.Before(facts.CompleteSince.Add(policy.CompleteGrace)) {
		return OrchestratorCompleteGrace
	}
	return OrchestratorPaused
}

type OrchestratorWakeReason string

const (
	OrchestratorWakeOpenWork      OrchestratorWakeReason = "open-work"
	OrchestratorWakeReviewRequest OrchestratorWakeReason = "review-request"
	OrchestratorWakeHumanAnswer   OrchestratorWakeReason = "human-answer"
	OrchestratorWakeRecovery      OrchestratorWakeReason = "recovery"
)

func (p OrchestratorLifecyclePolicy) WakeAllowed(lastWake, now time.Time) bool {
	return lastWake.IsZero() || p.WakeDebounce <= 0 || !now.Before(lastWake.Add(p.WakeDebounce))
}
