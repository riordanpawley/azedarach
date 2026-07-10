package domain

import "testing"

func TestPrematureRouteGuidanceIsConservative(t *testing.T) {
	tests := []struct {
		name       string
		assessment IssueExecutabilityAssessment
		wantOK     bool
	}{
		{name: "missing contract", assessment: IssueExecutabilityAssessment{Disposition: IssuePremature, Reasons: []string{"missing-scope", "missing-acceptance"}}, wantOK: true},
		{name: "dependency blocker", assessment: IssueExecutabilityAssessment{Disposition: IssuePremature, Reasons: []string{"missing-acceptance", "blocked-by:dep"}}},
		{name: "human judgment", assessment: IssueExecutabilityAssessment{Disposition: IssueNeedsInteraction, Reasons: []string{"product-judgment-required"}}},
		{name: "executable", assessment: IssueExecutabilityAssessment{Disposition: IssueExecutable, Executable: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guidance, ok := PrematureRouteGuidance(tt.assessment)
			if ok != tt.wantOK {
				t.Fatalf("ok = %t, guidance = %v", ok, guidance)
			}
		})
	}
}
