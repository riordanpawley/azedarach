package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	LearningConsolidationDuplicate = "duplicate"
	LearningConsolidationConflict  = "conflict"
)

type LearningConsolidationCandidate struct {
	ID            string
	Summary       string
	Status        string
	Private       bool
	ExpiresAt     *time.Time
	StaleAt       *time.Time
	Superseded    bool
	Consolidated  bool
	Deleted       bool
	TargetState   string
	TargetRetired bool
	TargetDrifted bool
}

type LearningConsolidationMatch struct {
	Kind, Reason string
	Score        int
}

type LearningConsolidationResolution struct {
	SuggestionStatus string
	Confirm          bool
	CanonicalID      string
	MemberIDs        []string
	ReviewNote       string
}

// ValidateLearningConsolidationResolution centralizes the human-confirmed
// lifecycle: only a pending suggestion can move, confirmation requires an
// explicit member as canonical, and every transition requires a human note.
func ValidateLearningConsolidationResolution(v LearningConsolidationResolution) error {
	if strings.TrimSpace(v.ReviewNote) == "" {
		return fmt.Errorf("review note is required")
	}
	if v.SuggestionStatus != "pending" {
		return fmt.Errorf("suggestion is already %s", v.SuggestionStatus)
	}
	if !v.Confirm {
		return nil
	}
	canonical := strings.TrimSpace(v.CanonicalID)
	if canonical == "" {
		return fmt.Errorf("canonical learning id is required")
	}
	for _, member := range v.MemberIDs {
		if canonical == member {
			return nil
		}
	}
	return fmt.Errorf("canonical learning must be a suggestion member")
}

// LearningConsolidationEligible is the lifecycle policy applied before the
// store asks its search index for possible pairs. Candidates may be reviewed;
// terminal or inactive guidance may not be suggested again.
func LearningConsolidationEligible(v LearningConsolidationCandidate, now time.Time) bool {
	if v.Private || v.Deleted || v.Consolidated || v.Superseded || v.TargetRetired || v.TargetDrifted {
		return false
	}
	if v.Status != "candidate" && v.Status != "accepted" && v.Status != "promoted" {
		return false
	}
	if v.ExpiresAt != nil && !v.ExpiresAt.After(now) || v.StaleAt != nil && !v.StaleAt.After(now) {
		return false
	}
	return v.Status != "promoted" || v.TargetState == "" || v.TargetState == "active"
}

func ClassifyLearningConsolidation(a, b string) (LearningConsolidationMatch, bool) {
	aw, an := learningConsolidationWords(a)
	bw, bn := learningConsolidationWords(b)
	if len(aw) == 0 || len(bw) == 0 {
		return LearningConsolidationMatch{}, false
	}
	intersection := 0
	union := map[string]struct{}{}
	for w := range aw {
		union[w] = struct{}{}
		if _, ok := bw[w]; ok {
			intersection++
		}
	}
	for w := range bw {
		union[w] = struct{}{}
	}
	score := intersection * 100 / len(union)
	if score >= 60 && an != bn {
		return LearningConsolidationMatch{Kind: LearningConsolidationConflict, Score: score, Reason: "high summary overlap with opposite negation"}, true
	}
	if score >= 75 {
		return LearningConsolidationMatch{Kind: LearningConsolidationDuplicate, Score: score, Reason: "high normalized summary overlap"}, true
	}
	return LearningConsolidationMatch{}, false
}

// LearningConsolidationSearchTerms exposes the classifier's normalization so
// indexed candidate retrieval cannot drift from final semantic filtering.
func LearningConsolidationSearchTerms(summary string) []string {
	words, _ := learningConsolidationWords(summary)
	out := make([]string, 0, len(words))
	for word := range words {
		out = append(out, word)
	}
	sort.Strings(out)
	return out
}

func learningConsolidationWords(s string) (map[string]struct{}, bool) {
	words := map[string]struct{}{}
	var b strings.Builder
	neg := false
	flush := func() {
		if b.Len() == 0 {
			return
		}
		w := b.String()
		b.Reset()
		switch w {
		case "not", "never", "avoid", "without", "mustnt", "dont", "cannot":
			neg = true
		default:
			if len([]rune(w)) > 2 {
				words[w] = struct{}{}
			}
		}
	}
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return words, neg
}
