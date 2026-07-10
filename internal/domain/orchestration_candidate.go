package domain

import (
	"strings"
	"time"
)

type OrchestrationCandidateClass string

const (
	OrchestrationCandidateOpen            OrchestrationCandidateClass = "open"
	OrchestrationCandidateActive          OrchestrationCandidateClass = "active"
	OrchestrationCandidateReviewReady     OrchestrationCandidateClass = "review-ready"
	OrchestrationCandidateDecisionWaiting OrchestrationCandidateClass = "decision-waiting"
	OrchestrationCandidateBacklog         OrchestrationCandidateClass = "backlog"
	OrchestrationCandidateOwnedElsewhere  OrchestrationCandidateClass = "owned-elsewhere"
	OrchestrationCandidateBlocked         OrchestrationCandidateClass = "blocked"
)

// OrchestrationCandidateAssessment is the shared semantic filter applied after
// indexed candidate selection. Keeping this in domain prevents store and daemon
// candidate rules from drifting.
type OrchestrationCandidateAssessment struct {
	Classification   OrchestrationCandidateClass
	Eligible         bool
	Sufficient       bool
	Sufficiency      []string
	ExclusionReasons []string
}

func AssessOrchestrationCandidate(task Task, actorID string, now time.Time, blockers []string) OrchestrationCandidateAssessment {
	facts := task.IssueFacts()
	a := OrchestrationCandidateAssessment{Classification: OrchestrationCandidateOpen, Eligible: true, Sufficient: true}
	if strings.TrimSpace(task.Title) != "" {
		a.Sufficiency = append(a.Sufficiency, "title-present")
	} else {
		a.Sufficient = false
		a.Sufficiency = append(a.Sufficiency, "missing-title")
	}
	if strings.TrimSpace(task.Description) != "" || strings.TrimSpace(task.Acceptance) != "" || strings.TrimSpace(task.Design) != "" {
		a.Sufficiency = append(a.Sufficiency, "execution-context-present")
	} else {
		a.Sufficient = false
		a.Sufficiency = append(a.Sufficiency, "missing-execution-context")
	}

	switch {
	case len(blockers) > 0:
		a.Classification, a.Eligible = OrchestrationCandidateBlocked, false
		a.ExclusionReasons = append(a.ExclusionReasons, blockers...)
	case task.Ownership != nil && task.Ownership.BlocksActor(actorID, now):
		a.Classification, a.Eligible = OrchestrationCandidateOwnedElsewhere, false
		a.ExclusionReasons = append(a.ExclusionReasons, "owned-by-"+strings.TrimSpace(task.Ownership.OwnerID))
	case facts.WaitingHuman:
		a.Classification, a.Eligible = OrchestrationCandidateDecisionWaiting, false
		a.ExclusionReasons = append(a.ExclusionReasons, "waiting-for-human-decision")
	case facts.ReviewReadyVisible || facts.ReviewState == IssueReviewRequested:
		a.Classification, a.Eligible = OrchestrationCandidateReviewReady, false
		a.ExclusionReasons = append(a.ExclusionReasons, "review-requested")
	case facts.LifecycleState == IssueWorkflowBacklog:
		a.Classification, a.Eligible = OrchestrationCandidateBacklog, false
		a.ExclusionReasons = append(a.ExclusionReasons, "lifecycle-backlog")
	case facts.HasActiveSession || facts.LifecycleState == IssueWorkflowActive:
		a.Classification, a.Eligible = OrchestrationCandidateActive, false
		a.ExclusionReasons = append(a.ExclusionReasons, "active-work-present")
	}
	return a
}
