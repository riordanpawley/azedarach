package domain

import (
	"testing"
	"time"
)

func TestNewLearningHealthRate(t *testing.T) {
	if got := NewLearningHealthRate(2, 4); got.Value != .5 || got.Numerator != 2 || got.Denominator != 4 {
		t.Fatalf("unexpected rate: %+v", got)
	}
	if got := NewLearningHealthRate(1, 0); got.Value != 0 {
		t.Fatalf("zero denominator must produce zero: %+v", got)
	}
}

func TestLearningHealthWindowAndDerivedMetrics(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	start, end := LearningHealthWindow(now)
	if end != now || start != now.AddDate(0, 0, -30) {
		t.Fatalf("window = %s..%s", start, end)
	}
	h := LearningPortfolioHealth{WindowDays: 30, PromotionEventCount: 3, DeliveredTokenCost: 20, UsefulDeliveryCount: 2}
	DeriveLearningHealthMetrics(&h, 2, 10, 3, 1, 4, 8, 2)
	if h.DuplicateDensity.Value != .2 || h.UsefulnessRate.Value != .75 || h.PromotionsPerDay != .1 || h.TokensPerUsefulActivation.Value != 10 {
		t.Fatalf("derived health = %+v", h)
	}
}
