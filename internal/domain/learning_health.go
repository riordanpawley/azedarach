package domain

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
	PromotionThroughput       LearningHealthRate `json:"promotion_throughput" msgpack:"promotion_throughput"`
	ContextualCoverage        LearningHealthRate `json:"contextual_coverage" msgpack:"contextual_coverage"`
	SelectionCount            int                `json:"selection_count" msgpack:"selection_count"`
	DeliveryCount             int                `json:"delivery_count" msgpack:"delivery_count"`
	UsefulDeliveryCount       int                `json:"useful_delivery_count" msgpack:"useful_delivery_count"`
	DeliveredTokenCost        int                `json:"delivered_token_cost" msgpack:"delivered_token_cost"`
	TokensPerUsefulActivation LearningHealthRate `json:"tokens_per_useful_activation" msgpack:"tokens_per_useful_activation"`
}
