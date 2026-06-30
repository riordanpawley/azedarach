package main

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestSummarizeSamples(t *testing.T) {
	t.Parallel()

	stats := summarize([]sample{
		{DurationMs: 1},
		{DurationMs: 2},
		{DurationMs: 10},
	})

	if stats.MinMs != 1 || stats.MaxMs != 10 {
		t.Fatalf("Min/Max = %f/%f, want 1/10", stats.MinMs, stats.MaxMs)
	}
	if math.Abs(stats.MeanMs-4.333) > 0.001 {
		t.Fatalf("MeanMs = %f, want 4.333", stats.MeanMs)
	}
	if stats.P50Ms != 2 {
		t.Fatalf("P50Ms = %f, want 2", stats.P50Ms)
	}
	if stats.P95Ms <= stats.P50Ms {
		t.Fatalf("P95Ms = %f, want greater than P50 %f", stats.P95Ms, stats.P50Ms)
	}
}

func TestRankBottlenecksOrdersByP95(t *testing.T) {
	t.Parallel()

	got := rankBottlenecks([]operationResult{
		{Name: "fast", Stats: timingStats{P95Ms: 1, MeanMs: 1}},
		{Name: "slow", Stats: timingStats{P95Ms: 10, MeanMs: 5}},
		{Name: "middle", Stats: timingStats{P95Ms: 3, MeanMs: 2}},
	}, 2)

	if len(got) != 2 {
		t.Fatalf("len(ranks) = %d, want 2", len(got))
	}
	if got[0].Name != "slow" || got[0].Rank != 1 {
		t.Fatalf("first rank = %+v, want slow rank 1", got[0])
	}
	if got[1].Name != "middle" || got[1].Rank != 2 {
		t.Fatalf("second rank = %+v, want middle rank 2", got[1])
	}
}

func TestRunHarnessSmallFixtureProducesMeasurementsAndPlans(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := runHarness(ctx, harnessConfig{
		ProjectID:       "proj-test",
		Iterations:      2,
		TaskCount:       80,
		RootCount:       2,
		ChildrenPerRoot: 12,
		BlockerEvery:    4,
		WorktreeEvery:   3,
		ActiveEvery:     7,
		BatchSize:       6,
		Timeout:         30 * time.Second,
	})
	if err != nil {
		t.Fatalf("runHarness() error = %v", err)
	}
	if result.SchemaVersion != outputSchema {
		t.Fatalf("SchemaVersion = %q, want %q", result.SchemaVersion, outputSchema)
	}
	if result.Fixture.IssueCount != 84 {
		t.Fatalf("IssueCount = %d, want roots + tasks + close candidates", result.Fixture.IssueCount)
	}
	if len(result.Operations) < 7 {
		t.Fatalf("operation count = %d, want at least 7", len(result.Operations))
	}
	for _, op := range result.Operations {
		if op.Iterations != 2 {
			t.Fatalf("%s iterations = %d, want 2", op.Name, op.Iterations)
		}
		if op.Stats.MaxMs < op.Stats.MinMs {
			t.Fatalf("%s max < min: %+v", op.Name, op.Stats)
		}
	}
	if len(result.Bottlenecks) != 3 {
		t.Fatalf("bottleneck count = %d, want 3", len(result.Bottlenecks))
	}
	if len(result.QueryPlans) < 7 {
		t.Fatalf("query plan count = %d, want at least 7", len(result.QueryPlans))
	}
	for _, plan := range result.QueryPlans {
		if len(plan.Rows) == 0 {
			t.Fatalf("%s has no query plan rows", plan.Name)
		}
	}
}
