package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestClassifyLearningConsolidation(t *testing.T) {
	tests := []struct {
		name, left, right, kind string
		ok                      bool
	}{
		{"duplicate", "Use bounded retries for daemon operations", "Use bounded retries for daemon operations", LearningConsolidationDuplicate, true},
		{"conflict", "Use automatic promotion after human review", "Never use automatic promotion after human review", LearningConsolidationConflict, true},
		{"unrelated", "Keep daemon ownership", "Render a narrow overlay", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ClassifyLearningConsolidation(tt.left, tt.right)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.kind, got.Kind)
		})
	}
}

func TestLearningConsolidationEligible(t *testing.T) {
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	future, past := now.Add(time.Hour), now.Add(-time.Hour)
	tests := []struct {
		name   string
		mutate func(*LearningConsolidationCandidate)
		want   bool
	}{
		{"candidate", func(*LearningConsolidationCandidate) {}, true},
		{"private", func(v *LearningConsolidationCandidate) { v.Private = true }, false},
		{"rejected", func(v *LearningConsolidationCandidate) { v.Status = "rejected" }, false},
		{"expired", func(v *LearningConsolidationCandidate) { v.ExpiresAt = &past }, false},
		{"future expiry", func(v *LearningConsolidationCandidate) { v.ExpiresAt = &future }, true},
		{"inactive promotion", func(v *LearningConsolidationCandidate) { v.Status = "promoted"; v.TargetState = "retired" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := LearningConsolidationCandidate{Status: "candidate"}
			tt.mutate(&v)
			assert.Equal(t, tt.want, LearningConsolidationEligible(v, now))
		})
	}
}

func TestValidateLearningConsolidationResolution(t *testing.T) {
	valid := LearningConsolidationResolution{SuggestionStatus: "pending", Confirm: true, CanonicalID: "learn-1", MemberIDs: []string{"learn-1", "learn-2"}, ReviewNote: "human confirmed"}
	assert.NoError(t, ValidateLearningConsolidationResolution(valid))
	invalid := valid
	invalid.CanonicalID = "learn-3"
	assert.EqualError(t, ValidateLearningConsolidationResolution(invalid), "canonical learning must be a suggestion member")
	invalid = valid
	invalid.SuggestionStatus = "confirmed"
	assert.EqualError(t, ValidateLearningConsolidationResolution(invalid), "suggestion is already confirmed")
}
