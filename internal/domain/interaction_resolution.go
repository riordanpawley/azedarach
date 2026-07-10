package domain

import (
	"fmt"
	"strings"
)

// InteractionResolutionPlan is the complete set of durable side effects
// authorized by a human final answer. Store implementations apply this plan in
// the same transaction that resolves the interaction.
type InteractionResolutionPlan struct {
	IssueEffects       InteractionIssueFieldEffects
	RequirementEffects []InteractionRequirementEffect
	Decision           *InteractionDecisionEffect
}

// PlanInteractionResolution converts the final answer audit into durable
// effects. Routine answers remain interaction evidence only. Material and
// critical significance permits explicitly approved durable effects but never
// implies that a decision or requirement mutation was approved.
func PlanInteractionResolution(request InteractionRequest) (InteractionResolutionPlan, error) {
	if request.State != InteractionResolved || request.FinalAnswer == nil {
		return InteractionResolutionPlan{}, fmt.Errorf("interaction resolution plan requires a resolved request with a final answer")
	}
	if !HumanInteractionActor(request.FinalAnswer.Actor) {
		return InteractionResolutionPlan{}, fmt.Errorf("%w: durable interaction effects require human confirmation", ErrInvalidInteractionAnswer)
	}
	answer := request.FinalAnswer.Answer
	plan := InteractionResolutionPlan{IssueEffects: answer.ApprovedIssueFieldEffects}
	significant := answer.SignificanceRecommendation == InteractionSignificanceMaterial || answer.SignificanceRecommendation == InteractionSignificanceCritical
	if !significant {
		if answer.ApprovedIssueFieldEffects.Any() || answer.ApprovedDecisionEffect != nil || len(answer.ApprovedRequirementEffects) > 0 {
			return InteractionResolutionPlan{}, fmt.Errorf("%w: routine interaction answers cannot apply durable effects", ErrInvalidInteractionAnswer)
		}
		return plan, nil
	}

	plan.RequirementEffects = append([]InteractionRequirementEffect(nil), answer.ApprovedRequirementEffects...)
	if answer.ApprovedDecisionEffect == nil {
		return plan, nil
	}
	decision := *answer.ApprovedDecisionEffect
	decision.ExistingDecisionID = strings.TrimSpace(decision.ExistingDecisionID)
	decision.Title = strings.TrimSpace(decision.Title)
	decision.Rationale = strings.TrimSpace(decision.Rationale)
	decision.Context = strings.TrimSpace(decision.Context)
	decision.Consequences = strings.TrimSpace(decision.Consequences)
	plan.Decision = &decision
	if plan.Decision.ExistingDecisionID == "" {
		if plan.Decision.Title == "" {
			plan.Decision.Title = strings.TrimSpace(request.DecisionPacket.Summary)
		}
		if plan.Decision.Rationale == "" {
			plan.Decision.Rationale = strings.TrimSpace(answer.Rationale)
		}
		if plan.Decision.Context == "" {
			plan.Decision.Context = strings.TrimSpace(strings.Join([]string{request.Why, request.Context}, "\n\n"))
		}
		if plan.Decision.Consequences == "" {
			plan.Decision.Consequences = strings.Join(answer.Constraints, "; ")
		}
		if plan.Decision.Title == "" || plan.Decision.Rationale == "" {
			return InteractionResolutionPlan{}, fmt.Errorf("%w: significant interaction requires decision title and rationale", ErrInvalidInteractionAnswer)
		}
	}
	return plan, nil
}

func HumanInteractionActor(actor string) bool {
	normalized := strings.ToLower(strings.TrimSpace(actor))
	return normalized == "human" || strings.HasPrefix(normalized, "human:")
}
