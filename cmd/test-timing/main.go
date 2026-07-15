package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/testtiming"
)

type packageList []string

func (p *packageList) String() string         { return strings.Join(*p, ",") }
func (p *packageList) Set(value string) error { *p = append(*p, value); return nil }

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "test-timing:", err)
		os.Exit(1)
	}
}

func run() error {
	var packages packageList
	profileName := flag.String("profile", "focused", "profile: "+strings.Join(testtiming.ProfileNames(), ", "))
	baselinePath := flag.String("baseline", "testdata/test-timing-baseline-2026-07-13.json", "committed baseline and budgets")
	outputRoot := flag.String("output", ".tmp/test-timing", "artifact root directory")
	runPattern := flag.String("run", "", "optional Go test regexp")
	checkBudgets := flag.Bool("check-budgets", true, "exit non-zero for timing budget violations")
	flag.Var(&packages, "package", "package pattern override; repeat for multiple patterns")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flag.Args(), " "))
	}

	profile, err := testtiming.ResolveProfile(*profileName, packages, *runPattern)
	if err != nil {
		return err
	}
	baseline, err := loadBaseline(*baselinePath)
	if err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	outputDir := filepath.Join(*outputRoot, profile.Name+"-"+stamp)
	measurement, runErr := testtiming.Run(context.Background(), testtiming.RunOptions{Profile: profile, Baseline: baseline, OutputDir: outputDir, WorkingDir: ".", CheckBudgets: *checkBudgets, PublishValidationEvidence: true})
	fmt.Printf("profile=%s wall=%.2fs packages=%d tests=%d failures=%d violations=%d\n", measurement.Profile, measurement.WallSeconds, len(measurement.Packages), len(measurement.Tests), len(measurement.Failures), len(measurement.Comparison.Violations))
	fmt.Printf("build_cache_namespace=%s before_bytes=%d after_bytes=%d delta_bytes=%d family_bytes=%d decision=%s\n", measurement.BuildCache.Namespace, measurement.BuildCache.Before.Bytes, measurement.BuildCache.After.Bytes, measurement.BuildCache.DeltaBytes, measurement.BuildCache.FamilyBytes, measurement.BuildCache.Decision)
	fmt.Printf("report=%s\nraw=%s\n", filepath.Join(outputDir, "report.md"), filepath.Join(outputDir, "events.jsonl"))
	return runErr
}

func loadBaseline(path string) (testtiming.Baseline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return testtiming.Baseline{}, fmt.Errorf("read baseline: %w", err)
	}
	var baseline testtiming.Baseline
	if err := json.Unmarshal(data, &baseline); err != nil {
		return testtiming.Baseline{}, fmt.Errorf("decode baseline: %w", err)
	}
	if err := testtiming.ValidateBaseline(baseline); err != nil {
		return testtiming.Baseline{}, err
	}
	return baseline, nil
}
