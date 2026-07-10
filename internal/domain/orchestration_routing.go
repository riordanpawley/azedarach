package domain

import (
	"fmt"
	"sort"
	"strings"
)

type OrchestrationRouteKind string

const (
	OrchestrationRouteBacklog     OrchestrationRouteKind = "backlog"
	OrchestrationRouteInteraction OrchestrationRouteKind = "interaction"
)

// OrchestrationCandidateRoute is an explicit project-steward mutation. It is
// deliberately separate from general issue lifecycle commands so ordinary
// issue clients cannot accidentally inherit project-orchestration policy.
type OrchestrationCandidateRoute struct {
	IssueID        string                 `json:"issue_id" msgpack:"issue_id"`
	Kind           OrchestrationRouteKind `json:"kind" msgpack:"kind"`
	Reason         string                 `json:"reason" msgpack:"reason"`
	MissingDetails []string               `json:"missing_details,omitempty" msgpack:"missing_details,omitempty"`
	Interaction    *InteractionRequest    `json:"interaction,omitempty" msgpack:"interaction,omitempty"`
}

func (r OrchestrationCandidateRoute) Validate() error {
	if strings.TrimSpace(r.IssueID) == "" {
		return fmt.Errorf("orchestration route issue id is required")
	}
	if strings.TrimSpace(r.Reason) == "" {
		return fmt.Errorf("orchestration route reason is required")
	}
	switch r.Kind {
	case OrchestrationRouteBacklog:
		if r.Interaction != nil {
			return fmt.Errorf("backlog route must not include an interaction")
		}
		if len(normalizedSorted(r.MissingDetails)) == 0 {
			return fmt.Errorf("backlog route requires missing-detail guidance")
		}
	case OrchestrationRouteInteraction:
		if r.Interaction == nil {
			return fmt.Errorf("interaction route requires an interaction request")
		}
		if r.Interaction.IssueID != strings.TrimSpace(r.IssueID) {
			return fmt.Errorf("interaction route issue %s does not match request issue %s", r.IssueID, r.Interaction.IssueID)
		}
		if !strings.EqualFold(strings.TrimSpace(r.Interaction.OrchestrationScope), string(OrchestrationScopeProject)) {
			return fmt.Errorf("routed interaction orchestration scope must be project")
		}
		if r.Interaction.State != InteractionOpen || r.Interaction.Revision != 1 || r.Interaction.Proposal != nil || r.Interaction.FinalAnswer != nil {
			return fmt.Errorf("routed interaction must start open at revision 1 without answers")
		}
		if err := r.Interaction.Validate(); err != nil {
			return fmt.Errorf("validate routed interaction: %w", err)
		}
	default:
		return fmt.Errorf("unsupported orchestration route kind %q", r.Kind)
	}
	return nil
}

// PrematureRouteGuidance permits automatic backlog routing only for missing
// contract fields. Dependency blockers and material unknowns are not evidence
// that work is clearly premature and must remain in their dedicated flows.
func PrematureRouteGuidance(assessment IssueExecutabilityAssessment) ([]string, bool) {
	if assessment.Disposition != IssuePremature || assessment.Executable {
		return nil, false
	}
	missing := make([]string, 0, 2)
	for _, reason := range assessment.Reasons {
		switch strings.TrimSpace(reason) {
		case "missing-scope":
			missing = append(missing, "add explicit implementation scope")
		case "missing-acceptance":
			missing = append(missing, "add explicit acceptance criteria")
		case "":
			continue
		default:
			return nil, false
		}
	}
	if len(missing) == 0 {
		return nil, false
	}
	sort.Strings(missing)
	return missing, true
}
