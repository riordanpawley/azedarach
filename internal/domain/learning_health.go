package domain

import "time"

const LearningHealthWindowDays = 30

func LearningHealthWindow(now time.Time) (time.Time, time.Time) {
	end := now.UTC()
	return end.AddDate(0, 0, -LearningHealthWindowDays), end
}

// LearningHealthRate makes every reported ratio auditable at the API boundary.
// Value is zero when Denominator is zero.
type LearningHealthRate struct {
	Numerator   int     `json:"numerator" msgpack:"numerator"`
	Denominator int     `json:"denominator" msgpack:"denominator"`
	Value       float64 `json:"value" msgpack:"value"`
}

func NewLearningHealthRate(numerator, denominator int) LearningHealthRate {
	r := LearningHealthRate{Numerator: numerator, Denominator: denominator}
	if denominator > 0 {
		r.Value = float64(numerator) / float64(denominator)
	}
	return r
}

type LearningPortfolioHealth struct {
	ProjectID                 string             `json:"project_id" msgpack:"project_id"`
	GeneratedAt               string             `json:"generated_at" msgpack:"generated_at"`
	CandidateCount            int                `json:"candidate_count" msgpack:"candidate_count"`
	CandidateAgeAverageHours  float64            `json:"candidate_age_average_hours" msgpack:"candidate_age_average_hours"`
	CandidateAgeMaximumHours  float64            `json:"candidate_age_maximum_hours" msgpack:"candidate_age_maximum_hours"`
	DuplicateDensity          LearningHealthRate `json:"duplicate_density" msgpack:"duplicate_density"`
	UsefulnessRate            LearningHealthRate `json:"usefulness_rate" msgpack:"usefulness_rate"`
	ContradictionRate         LearningHealthRate `json:"contradiction_rate" msgpack:"contradiction_rate"`
	PromotionConversion       LearningHealthRate `json:"promotion_conversion" msgpack:"promotion_conversion"`
	ContextualCoverage        LearningHealthRate `json:"contextual_coverage" msgpack:"contextual_coverage"`
	SelectionCount            int                `json:"selection_count" msgpack:"selection_count"`
	DeliveryCount             int                `json:"delivery_count" msgpack:"delivery_count"`
	UsefulDeliveryCount       int                `json:"useful_delivery_count" msgpack:"useful_delivery_count"`
	DeliveredTokenCost        int                `json:"delivered_token_cost" msgpack:"delivered_token_cost"`
	TokensPerUsefulActivation LearningHealthRate `json:"tokens_per_useful_activation" msgpack:"tokens_per_useful_activation"`
	WindowStart               string             `json:"window_start" msgpack:"window_start"`
	WindowEnd                 string             `json:"window_end" msgpack:"window_end"`
	WindowDays                int                `json:"window_days" msgpack:"window_days"`
	ProposalCount             int                `json:"proposal_count" msgpack:"proposal_count"`
	AbandonedProposalCount    int                `json:"abandoned_proposal_count" msgpack:"abandoned_proposal_count"`
	PendingProposalCount      int                `json:"pending_proposal_count" msgpack:"pending_proposal_count"`
	SuppressionExclusionCount int                `json:"suppression_exclusion_count" msgpack:"suppression_exclusion_count"`
	BudgetExclusionCount      int                `json:"budget_exclusion_count" msgpack:"budget_exclusion_count"`
	PromotionEventCount       int                `json:"promotion_event_count" msgpack:"promotion_event_count"`
	PromotionsPerDay          float64            `json:"promotions_per_day" msgpack:"promotions_per_day"`
}

// DeriveLearningHealthMetrics owns rate/throughput semantics after the store
// supplies privacy-filtered counts for the declared population and window.
func DeriveLearningHealthMetrics(h *LearningPortfolioHealth, duplicateCount, duplicatePairs, useful, contradicted, labeled, reviewed, promoted int) {
	h.DuplicateDensity = NewLearningHealthRate(duplicateCount, duplicatePairs)
	h.UsefulnessRate = NewLearningHealthRate(useful, labeled)
	h.ContradictionRate = NewLearningHealthRate(contradicted, labeled)
	h.PromotionConversion = NewLearningHealthRate(promoted, reviewed)
	h.PromotionsPerDay = float64(h.PromotionEventCount) / float64(h.WindowDays)
	h.TokensPerUsefulActivation = NewLearningHealthRate(h.DeliveredTokenCost, h.UsefulDeliveryCount)
}
