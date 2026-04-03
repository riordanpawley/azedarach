package main

import (
	"math"
	"testing"
)

func TestParseWorktreeListPorcelain(t *testing.T) {
	t.Parallel()

	raw := "" +
		"worktree /tmp/repo\n" +
		"HEAD abcdef\n" +
		"branch refs/heads/main\n" +
		"\n" +
		"worktree /tmp/repo-az-1\n" +
		"HEAD 123456\n" +
		"branch refs/heads/riordan/az-1/task\n" +
		"\n"

	got, err := parseWorktreeListPorcelain(raw)
	if err != nil {
		t.Fatalf("parseWorktreeListPorcelain() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(worktrees) = %d, want 2", len(got))
	}
	if got[0].Path == "" || got[1].Path == "" {
		t.Fatalf("expected absolute/normalized paths, got %+v", got)
	}
	if got[0].Branch != "main" {
		t.Fatalf("first branch = %q, want main", got[0].Branch)
	}
	if got[1].Branch != "riordan/az-1/task" {
		t.Fatalf("second branch = %q, want riordan/az-1/task", got[1].Branch)
	}
}

func TestParseWorktreeListPorcelainRejectsEmpty(t *testing.T) {
	t.Parallel()
	if _, err := parseWorktreeListPorcelain(""); err == nil {
		t.Fatal("expected error for empty porcelain output")
	}
}

func TestSummarizeSamples(t *testing.T) {
	t.Parallel()

	stats := summarize([]sample{
		{DurationMs: 10, Success: true},
		{DurationMs: 20, Success: false, Error: "boom"},
		{DurationMs: 30, Success: false, Timeout: true, Error: "timeout"},
		{DurationMs: 40, Success: true},
	})

	if stats.Count != 4 {
		t.Fatalf("Count = %d, want 4", stats.Count)
	}
	if stats.Failures != 2 {
		t.Fatalf("Failures = %d, want 2", stats.Failures)
	}
	if stats.Timeouts != 1 {
		t.Fatalf("Timeouts = %d, want 1", stats.Timeouts)
	}
	if stats.MinMs != 10 || stats.MaxMs != 40 {
		t.Fatalf("Min/Max = %f/%f, want 10/40", stats.MinMs, stats.MaxMs)
	}
	if math.Abs(stats.MeanMs-25) > 0.0001 {
		t.Fatalf("MeanMs = %f, want 25", stats.MeanMs)
	}
	if stats.P50Ms <= 0 || stats.P95Ms <= 0 || stats.P99Ms <= 0 {
		t.Fatalf("expected non-zero percentiles, got %+v", stats)
	}
}

