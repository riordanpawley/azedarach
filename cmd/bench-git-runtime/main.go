package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type worktreeInfo struct {
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
}

type sample struct {
	DurationMs float64 `json:"duration_ms"`
	Success    bool    `json:"success"`
	Timeout    bool    `json:"timeout"`
	Error      string  `json:"error,omitempty"`
}

type opStats struct {
	Count    int     `json:"count"`
	Failures int     `json:"failures"`
	Timeouts int     `json:"timeouts"`
	MinMs    float64 `json:"min_ms"`
	P50Ms    float64 `json:"p50_ms"`
	P95Ms    float64 `json:"p95_ms"`
	P99Ms    float64 `json:"p99_ms"`
	MaxMs    float64 `json:"max_ms"`
	MeanMs   float64 `json:"mean_ms"`
}

type opResult struct {
	Stats   opStats   `json:"stats"`
	Samples []sample  `json:"samples,omitempty"`
}

type benchmarkResult struct {
	StartedAt           time.Time             `json:"started_at"`
	FinishedAt          time.Time             `json:"finished_at"`
	RepoRoot            string                `json:"repo_root"`
	BaseBranch          string                `json:"base_branch"`
	Iterations          int                   `json:"iterations"`
	WorktreeCount       int                   `json:"worktree_count"`
	PerCommandTimeoutMs int64                 `json:"per_command_timeout_ms"`
	Operations          map[string]opResult   `json:"operations"`
	Worktrees           []worktreeInfo        `json:"worktrees"`
}

func main() {
	repoFlag := flag.String("repo", ".", "repository path")
	baseFlag := flag.String("base", "main", "base branch for ahead/behind and merge-base checks")
	iterationsFlag := flag.Int("iterations", 2, "number of benchmark iterations")
	maxWorktreesFlag := flag.Int("max-worktrees", 0, "max number of worktrees to benchmark (0 = all)")
	perCommandTimeoutFlag := flag.Duration("timeout", 10*time.Second, "per-command timeout")
	includeSamplesFlag := flag.Bool("samples", false, "include per-sample timings in output")
	flag.Parse()

	if *iterationsFlag < 1 {
		exitf("iterations must be >= 1")
	}
	if *perCommandTimeoutFlag <= 0 {
		exitf("timeout must be > 0")
	}

	repoRoot, err := absPath(*repoFlag)
	if err != nil {
		exitf("resolve repo path: %v", err)
	}

	worktrees, err := listWorktrees(repoRoot)
	if err != nil {
		exitf("list worktrees: %v", err)
	}
	if len(worktrees) == 0 {
		exitf("no worktrees found for %s", repoRoot)
	}
	if *maxWorktreesFlag > 0 && len(worktrees) > *maxWorktreesFlag {
		worktrees = worktrees[:*maxWorktreesFlag]
	}

	startedAt := time.Now().UTC()
	result := benchmarkResult{
		StartedAt:           startedAt,
		RepoRoot:            repoRoot,
		BaseBranch:          strings.TrimSpace(*baseFlag),
		Iterations:          *iterationsFlag,
		WorktreeCount:       len(worktrees),
		PerCommandTimeoutMs: (*perCommandTimeoutFlag).Milliseconds(),
		Operations:          make(map[string]opResult),
		Worktrees:           worktrees,
	}

	rawSamples := map[string][]sample{
		"worktree_list": {},
		"status":        {},
		"merge_base":    {},
		"rev_list_ahead":  {},
		"rev_list_behind": {},
		"diff_shortstat":  {},
	}

	for i := 0; i < *iterationsFlag; i++ {
		s := runCommand(repoRoot, *perCommandTimeoutFlag, "git", "-C", repoRoot, "worktree", "list", "--porcelain")
		rawSamples["worktree_list"] = append(rawSamples["worktree_list"], s)

		for _, wt := range worktrees {
			statusSample := runCommand(repoRoot, *perCommandTimeoutFlag, "git", "-C", wt.Path, "status", "--porcelain")
			rawSamples["status"] = append(rawSamples["status"], statusSample)

			mergeBaseSample, mergeBase := runCommandOutput(repoRoot, *perCommandTimeoutFlag, "git", "-C", wt.Path, "merge-base", *baseFlag, "HEAD")
			rawSamples["merge_base"] = append(rawSamples["merge_base"], mergeBaseSample)

			behindSample := runCommand(repoRoot, *perCommandTimeoutFlag, "git", "-C", wt.Path, "rev-list", "--count", "HEAD.."+*baseFlag)
			rawSamples["rev_list_behind"] = append(rawSamples["rev_list_behind"], behindSample)

			aheadSample := runCommand(repoRoot, *perCommandTimeoutFlag, "git", "-C", wt.Path, "rev-list", "--count", *baseFlag+"..HEAD")
			rawSamples["rev_list_ahead"] = append(rawSamples["rev_list_ahead"], aheadSample)

			diffArgs := []string{"git", "-C", wt.Path, "diff", "--shortstat"}
			if mergeBaseSample.Success && mergeBase != "" {
				diffArgs = []string{"git", "-C", wt.Path, "diff", "--shortstat", mergeBase, "HEAD", "--", ":^.azedarach"}
			}
			diffSample := runCommand(repoRoot, *perCommandTimeoutFlag, diffArgs[0], diffArgs[1:]...)
			rawSamples["diff_shortstat"] = append(rawSamples["diff_shortstat"], diffSample)
		}
	}

	for op, samples := range rawSamples {
		entry := opResult{Stats: summarize(samples)}
		if *includeSamplesFlag {
			entry.Samples = samples
		}
		result.Operations[op] = entry
	}
	result.FinishedAt = time.Now().UTC()

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		exitf("encode result: %v", err)
	}
}

func listWorktrees(repoRoot string) ([]worktreeInfo, error) {
	cmd := exec.Command("git", "-C", repoRoot, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git worktree list --porcelain failed: %w", err)
	}
	return parseWorktreeListPorcelain(string(out))
}

func parseWorktreeListPorcelain(raw string) ([]worktreeInfo, error) {
	lines := strings.Split(raw, "\n")
	var worktrees []worktreeInfo
	var current worktreeInfo
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "worktree "):
			if current.Path != "" {
				worktrees = append(worktrees, current)
			}
			current = worktreeInfo{Path: strings.TrimSpace(strings.TrimPrefix(line, "worktree "))}
		case strings.HasPrefix(line, "branch "):
			current.Branch = strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(line, "branch ")), "refs/heads/")
		case line == "":
			if current.Path != "" {
				worktrees = append(worktrees, current)
				current = worktreeInfo{}
			}
		}
	}
	if current.Path != "" {
		worktrees = append(worktrees, current)
	}
	if len(worktrees) == 0 {
		return nil, fmt.Errorf("no worktrees parsed from porcelain output")
	}
	for i := range worktrees {
		if abs, err := absPath(worktrees[i].Path); err == nil {
			worktrees[i].Path = abs
		}
	}
	return worktrees, nil
}

func runCommand(repoRoot string, timeout time.Duration, bin string, args ...string) sample {
	s, _ := runCommandOutput(repoRoot, timeout, bin, args...)
	return s
}

func runCommandOutput(repoRoot string, timeout time.Duration, bin string, args ...string) (sample, string) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = repoRoot
	start := time.Now()
	out, err := cmd.Output()
	durationMs := float64(time.Since(start).Microseconds()) / 1000.0
	s := sample{
		DurationMs: durationMs,
		Success:    err == nil,
	}
	if err == nil {
		return s, strings.TrimSpace(string(out))
	}

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		s.Timeout = true
		s.Error = "timeout"
		return s, ""
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		stderr := strings.TrimSpace(string(exitErr.Stderr))
		if stderr != "" {
			s.Error = stderr
		} else {
			s.Error = exitErr.Error()
		}
		return s, ""
	}
	s.Error = err.Error()
	return s, ""
}

func summarize(samples []sample) opStats {
	stats := opStats{Count: len(samples)}
	if len(samples) == 0 {
		return stats
	}
	durations := make([]float64, 0, len(samples))
	sum := 0.0
	for _, s := range samples {
		durations = append(durations, s.DurationMs)
		sum += s.DurationMs
		if !s.Success {
			stats.Failures++
		}
		if s.Timeout {
			stats.Timeouts++
		}
	}
	sort.Float64s(durations)
	stats.MinMs = durations[0]
	stats.MaxMs = durations[len(durations)-1]
	stats.MeanMs = sum / float64(len(durations))
	stats.P50Ms = percentile(durations, 0.50)
	stats.P95Ms = percentile(durations, 0.95)
	stats.P99Ms = percentile(durations, 0.99)
	return stats
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	rank := p * float64(len(sorted)-1)
	lower := int(math.Floor(rank))
	upper := int(math.Ceil(rank))
	if lower == upper {
		return sorted[lower]
	}
	weight := rank - float64(lower)
	return sorted[lower] + (sorted[upper]-sorted[lower])*weight
}

func absPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("empty path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func exitf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
