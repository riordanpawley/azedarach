package domain

import "testing"

func TestPlanInteractionResolutionSignificanceGate(t *testing.T) {
	r := validInteractionRequest()
	answer := interactionTestAnswer("squash", 1)
	answer.SignificanceRecommendation = InteractionSignificanceRoutine
	r.FinalAnswer = &InteractionAnswerAudit{Answer: answer, Actor: "human", CreatedAt: r.UpdatedAt}
	r.State, r.Revision = InteractionResolved, 2

	plan, err := PlanInteractionResolution(r)
	if err != nil || plan.Decision != nil || len(plan.RequirementEffects) != 0 {
		t.Fatalf("routine plan = %+v, err=%v", plan, err)
	}

	r.FinalAnswer.Answer.SignificanceRecommendation = InteractionSignificanceMaterial
	plan, err = PlanInteractionResolution(r)
	if err != nil || plan.Decision == nil || plan.Decision.Title != r.DecisionPacket.Summary || plan.Decision.Rationale != answer.Rationale {
		t.Fatalf("significant plan = %+v, err=%v", plan, err)
	}
}

func TestPlanInteractionResolutionRejectsRoutineSpecAndDecisionEffects(t *testing.T) {
	r := validInteractionRequest()
	answer := interactionTestAnswer("squash", 1)
	answer.SignificanceRecommendation = InteractionSignificanceRoutine
	description := "changed behavior"
	answer.ApprovedRequirementEffects = []InteractionRequirementEffect{{RequirementID: "fr-1", Description: &description}}
	r.FinalAnswer = &InteractionAnswerAudit{Answer: answer, Actor: "human", CreatedAt: r.UpdatedAt}
	r.State, r.Revision = InteractionResolved, 2
	if _, err := PlanInteractionResolution(r); err == nil {
		t.Fatal("routine answer accepted a requirement effect")
	}
}

func TestPlanInteractionResolutionRejectsRoutineIssueEffects(t *testing.T) {
	r := validInteractionRequest()
	answer := interactionTestAnswer("squash", 1)
	answer.SignificanceRecommendation = InteractionSignificanceRoutine
	description := "changed issue contract"
	answer.ApprovedIssueFieldEffects.Description = &description
	r.FinalAnswer = &InteractionAnswerAudit{Answer: answer, Actor: "human", CreatedAt: r.UpdatedAt}
	r.State, r.Revision = InteractionResolved, 2
	if _, err := PlanInteractionResolution(r); err == nil {
		t.Fatal("routine answer accepted an issue effect")
	}
}

func TestPlanInteractionResolutionRequiresHumanFinalAudit(t *testing.T) {
	r := validInteractionRequest()
	answer := interactionTestAnswer("squash", 1)
	r.FinalAnswer = &InteractionAnswerAudit{Answer: answer, Actor: "advisor:session", CreatedAt: r.UpdatedAt}
	r.State, r.Revision = InteractionResolved, 2
	if _, err := PlanInteractionResolution(r); err == nil {
		t.Fatal("advisor-authored final audit produced a durable resolution plan")
	}
}
