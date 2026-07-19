package main

import (
	"context"
	"encoding/json"
	"errors"
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
	checkBudgets := flag.Bool("check-budgets", true, "enforce timing budgets in controlled ci-timing; local profiles remain diagnostic")
	samples := flag.Int("samples", 1, "number of samples; controlled ci-timing requires at least 3")
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
	enforce := profile.Name == "ci-timing"
	if !enforce {
		*checkBudgets = false
		if *samples != 1 {
			return fmt.Errorf("--samples is available only with ci-timing")
		}
	} else if err := testtiming.ControlledEnvironment(*samples); err != nil {
		return err
	}
	measurements := make([]testtiming.Measurement, 0, *samples)
	var runErr error
	for i := 1; i <= *samples; i++ {
		sampleDir := outputDir
		if *samples > 1 {
			sampleDir = filepath.Join(outputDir, fmt.Sprintf("sample-%02d", i))
		}
		measurement, err := testtiming.Run(context.Background(), testtiming.RunOptions{Profile: profile, Baseline: baseline, OutputDir: sampleDir, WorkingDir: ".", CheckBudgets: false, PublishValidationEvidence: true})
		measurements = append(measurements, measurement)
		if err != nil {
			runErr = errors.Join(runErr, err)
		}
	}
	measurement := measurements[0]
	reportPath := filepath.Join(outputDir, "report.md")
	if enforce && runErr == nil {
		measurement, err = testtiming.AggregateMedian(measurements, baseline)
		if err != nil {
			return err
		}
		if err := testtiming.WriteArtifacts(outputDir, measurement); err != nil {
			return err
		}
		if *checkBudgets && len(measurement.Comparison.Violations) > 0 {
			runErr = fmt.Errorf("%d controlled timing budget violation(s)", len(measurement.Comparison.Violations))
		}
	} else if enforce {
		reportPath = filepath.Join(outputDir, "sample-01", "report.md")
	}
	fmt.Printf("profile=%s wall=%.2fs packages=%d tests=%d failures=%d violations=%d\n", measurement.Profile, measurement.WallSeconds, len(measurement.Packages), len(measurement.Tests), len(measurement.Failures), len(measurement.Comparison.Violations))
	fmt.Printf("build_cache_namespace=%s before_bytes=%d after_bytes=%d delta_bytes=%d family_bytes=%d decision=%s\n", measurement.BuildCache.Namespace, measurement.BuildCache.Before.Bytes, measurement.BuildCache.After.Bytes, measurement.BuildCache.DeltaBytes, measurement.BuildCache.FamilyBytes, measurement.BuildCache.Decision)
	fmt.Printf("clone_isolation_mode=%s configured=%t package_identities=%d\n", measurement.CloneIsolation.Mode, measurement.CloneIsolation.Configured, len(measurement.CloneIsolation.Packages))
	for _, identity := range measurement.CloneIsolation.Packages {
		fmt.Printf("clone_package=%s user=%s projects=%s\n", identity.Package, identity.UserDB, strings.Join(identity.ProjectDBs, string(os.PathListSeparator)))
	}
	if enforce {
		fmt.Printf("aggregate=median samples=%d\n", *samples)
	}
	fmt.Printf("report=%s\nartifacts=%s\n", reportPath, outputDir)
	if failureSummary := testtiming.FailureSummary(measurement.Failures); failureSummary != "" {
		fmt.Fprintf(os.Stderr, "actionable test failures:\n%s\n", failureSummary)
	}
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
