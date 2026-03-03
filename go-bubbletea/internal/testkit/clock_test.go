package testkit

import (
	"testing"
	"time"
)

func TestDeterministicClockNowSetAndAdvance(t *testing.T) {
	start := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	clock := NewDeterministicClock(start)

	AssertEqual(t, clock.Now(), start, "clock should return initial value")

	next := start.Add(2 * time.Hour)
	clock.Set(next)
	AssertEqual(t, clock.Now(), next, "clock should return set value")

	advanced := clock.Advance(30 * time.Second)
	AssertEqual(t, advanced, next.Add(30*time.Second), "advance should return updated value")
	AssertEqual(t, clock.Now(), next.Add(30*time.Second), "now should match advanced value")
}
