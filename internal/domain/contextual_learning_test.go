package domain

import "testing"

func TestSelectContextualLearningsDeterministicBudgetAndSuppression(t *testing.T) {
	candidates := []LearningActivationCandidate{{ID: "b", Summary: "12345678", Score: 10, Reason: "tag"}, {ID: "a", Summary: "12345678", Score: 10, Reason: "issue"}, {ID: "c", Summary: "12345678", Score: 9, Reason: "recent"}}
	got := SelectContextualLearnings(candidates, map[string]struct{}{"a": {}}, 4)
	if len(got.IDs) != 1 || got.IDs[0] != "b" || got.TokenCost != 4 || got.Explanations[0] != "b: tag" {
		t.Fatalf("selection = %+v", got)
	}
}
