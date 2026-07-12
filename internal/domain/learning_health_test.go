package domain

import "testing"

func TestNewLearningHealthRate(t *testing.T) {
	if got := NewLearningHealthRate(2, 4); got.Value != .5 || got.Numerator != 2 || got.Denominator != 4 {
		t.Fatalf("unexpected rate: %+v", got)
	}
	if got := NewLearningHealthRate(1, 0); got.Value != 0 {
		t.Fatalf("zero denominator must produce zero: %+v", got)
	}
}
