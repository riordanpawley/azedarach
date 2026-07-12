package domain

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

type LearningActivationPurpose string

const (
	LearningPurposeSessionStart      LearningActivationPurpose = "session_start"
	LearningPurposeContextTransition LearningActivationPurpose = "context_transition"
)

func (p LearningActivationPurpose) Valid() bool {
	return p == LearningPurposeSessionStart || p == LearningPurposeContextTransition
}

type LearningActivationCandidate struct {
	ID, Summary, Reason string
	Score               int
}

type LearningActivationSelection struct {
	IDs, Explanations []string
	TokenCost         int
}

// SelectContextualLearnings deterministically packs the highest-ranked guidance
// into the caller's token budget. Suppressed IDs are session-scoped deliveries.
func SelectContextualLearnings(candidates []LearningActivationCandidate, suppressed map[string]struct{}, tokenBudget int) LearningActivationSelection {
	if tokenBudget <= 0 {
		return LearningActivationSelection{}
	}
	sorted := append([]LearningActivationCandidate(nil), candidates...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Score != sorted[j].Score {
			return sorted[i].Score > sorted[j].Score
		}
		return sorted[i].ID < sorted[j].ID
	})
	var out LearningActivationSelection
	for _, candidate := range sorted {
		if _, skip := suppressed[candidate.ID]; skip || strings.TrimSpace(candidate.ID) == "" {
			continue
		}
		cost := contextualLearningTokenCost("- " + candidate.ID + ": " + candidate.Summary)
		if out.TokenCost+cost > tokenBudget {
			continue
		}
		out.IDs = append(out.IDs, candidate.ID)
		out.Explanations = append(out.Explanations, fmt.Sprintf("%s: %s", candidate.ID, strings.TrimSpace(candidate.Reason)))
		out.TokenCost += cost
	}
	return out
}

func contextualLearningTokenCost(value string) int {
	// A deterministic, conservative approximation suitable for a hard budget.
	runes := utf8.RuneCountInString(strings.TrimSpace(value))
	if runes == 0 {
		return 1
	}
	return (runes + 3) / 4
}

// RenderedLearningTokenCost measures the payload that the delivery adapter
// actually produced, rather than the daemon's pre-render selection estimate.
func RenderedLearningTokenCost(value string) int { return contextualLearningTokenCost(value) }
