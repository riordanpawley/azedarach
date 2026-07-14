package testtiming

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/gocache"
	"github.com/riordanpawley/azedarach/internal/testisolation"
)

type RunOptions struct {
	Profile      Profile
	Baseline     Baseline
	OutputDir    string
	WorkingDir   string
	CheckBudgets bool
	Now          func() time.Time
}

func Run(ctx context.Context, opts RunOptions) (Measurement, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.OutputDir == "" {
		return Measurement{}, fmt.Errorf("output directory is required")
	}
	cacheConfig, err := gocache.FromEnvironment(gocache.KindForProfile(opts.Profile.Name))
	if err != nil {
		return Measurement{}, fmt.Errorf("resolve Go build-cache protocol: %w", err)
	}
	var measurement Measurement
	var runErr error
	lockErr := gocache.WithExclusiveLock(ctx, cacheConfig, func() error {
		telemetry, err := gocache.Prepare(ctx, cacheConfig, os.Getenv("AZEDARACH_GO_CACHE_AUTO_MAINTAIN") == "1")
		if err != nil {
			measurement, runErr = writeRefusalArtifacts(opts, telemetry, err)
			return nil
		}
		measurement, runErr = runLocked(ctx, opts, cacheConfig, telemetry)
		return nil
	})
	if lockErr != nil {
		return measurement, lockErr
	}
	return measurement, runErr
}

func writeRefusalArtifacts(opts RunOptions, telemetry gocache.Telemetry, refusal error) (Measurement, error) {
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return Measurement{}, errors.Join(refusal, fmt.Errorf("create refusal artifact directory: %w", err))
	}
	rawPath := filepath.Join(opts.OutputDir, "events.jsonl")
	stderrPath := filepath.Join(opts.OutputDir, "stderr.txt")
	if err := os.WriteFile(rawPath, nil, 0o644); err != nil {
		return Measurement{}, errors.Join(refusal, err)
	}
	if err := os.WriteFile(stderrPath, []byte(refusal.Error()+"\n"), 0o644); err != nil {
		return Measurement{}, errors.Join(refusal, err)
	}
	measurement := Measurement{
		Schema:              ReportSchema,
		Profile:             opts.Profile.Name,
		CacheMode:           opts.Profile.CachePolicy(),
		TestResultCacheMode: opts.Profile.CachePolicy(),
		BuildCache:          telemetry,
		ResourceMethod:      "direct-go-command-process-state-v1",
		StartedAt:           opts.Now().UTC(),
		ExitCode:            1,
		RawJSONPath:         rawPath,
		StderrPath:          stderrPath,
		Command:             opts.Profile.Command(),
		Comparison:          Comparison{BaselineRecordedAt: opts.Baseline.RecordedAt, PackageDeltas: []Delta{}, Violations: []Violation{}},
	}
	if err := writeArtifacts(opts.OutputDir, measurement); err != nil {
		return measurement, errors.Join(refusal, err)
	}
	return measurement, refusal
}

func runLocked(ctx context.Context, opts RunOptions, cacheConfig gocache.Config, cacheTelemetry gocache.Telemetry) (Measurement, error) {
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return Measurement{}, fmt.Errorf("create output directory: %w", err)
	}
	isolation, err := testisolation.NewTemporary(opts.WorkingDir)
	if err != nil {
		return Measurement{}, fmt.Errorf("prepare test database isolation: %w", err)
	}
	defer isolation.Close()
	if opts.Profile.CleanCache {
		cmd := exec.CommandContext(ctx, "go", "clean", "-testcache")
		cmd.Dir = opts.WorkingDir
		cmd.Env = withEnv(os.Environ(), "GOCACHE", cacheConfig.CachePath())
		if output, err := cmd.CombinedOutput(); err != nil {
			return Measurement{}, fmt.Errorf("clean Go test cache: %w: %s", err, output)
		}
	}

	rawPath := filepath.Join(opts.OutputDir, "events.jsonl")
	stderrPath := filepath.Join(opts.OutputDir, "stderr.txt")
	raw, err := os.Create(rawPath)
	if err != nil {
		return Measurement{}, fmt.Errorf("create raw event file: %w", err)
	}
	defer raw.Close()
	stderr, err := os.Create(stderrPath)
	if err != nil {
		return Measurement{}, fmt.Errorf("create stderr file: %w", err)
	}
	defer stderr.Close()

	command := opts.Profile.Command()
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = opts.WorkingDir
	cmd.Env = isolation.Environ(withEnv(os.Environ(), "GOCACHE", cacheConfig.CachePath()))
	collector := NewEventCollector(raw)
	cmd.Stdout = collector
	cmd.Stderr = stderr
	startedAt := opts.Now().UTC()
	started := time.Now()
	runErr := cmd.Run()
	wall := time.Since(started).Seconds()
	collector.Finish()
	packages, tests, failures, invalid := collector.Results()
	exitCode := 0
	if runErr != nil {
		exitCode = 1
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}
	cacheTelemetry, cacheErr := gocache.Finish(cacheConfig, cacheTelemetry)
	m := Measurement{Schema: ReportSchema, Profile: opts.Profile.Name, CacheMode: opts.Profile.CachePolicy(), TestResultCacheMode: opts.Profile.CachePolicy(), BuildCache: cacheTelemetry, ResourceMethod: "direct-go-command-process-state-v1", StartedAt: startedAt, WallSeconds: wall, ExitCode: exitCode, Packages: packages, Tests: tests, Failures: failures, InvalidEvents: invalid, RawJSONPath: rawPath, StderrPath: stderrPath, Command: command}
	if cmd.ProcessState != nil {
		m.UserCPUSeconds = cmd.ProcessState.UserTime().Seconds()
		m.SystemCPUSeconds = cmd.ProcessState.SystemTime().Seconds()
		m.PeakRSSBytes = peakRSSBytes(cmd.ProcessState)
	}
	m.Comparison = Compare(m, opts.Baseline)
	if err := writeArtifacts(opts.OutputDir, m); err != nil {
		return m, err
	}
	var outcomes []error
	if cacheErr != nil {
		outcomes = append(outcomes, fmt.Errorf("measure Go build cache after validation: %w", cacheErr))
	} else if cacheTelemetry.FamilyBytes > cacheConfig.HardLimitBytes {
		outcomes = append(outcomes, fmt.Errorf("Go build-cache family grew to %d bytes, above hard limit %d", cacheTelemetry.FamilyBytes, cacheConfig.HardLimitBytes))
	}
	if runErr != nil {
		outcomes = append(outcomes, fmt.Errorf("test command exited %d: %w", exitCode, runErr))
	}
	if invalid > 0 {
		outcomes = append(outcomes, fmt.Errorf("test command emitted %d invalid JSON event line(s)", invalid))
	}
	if opts.CheckBudgets && len(m.Comparison.Violations) > 0 {
		outcomes = append(outcomes, fmt.Errorf("%d timing budget violation(s)", len(m.Comparison.Violations)))
	}
	return m, errors.Join(outcomes...)
}

func withEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			out = append(out, item)
		}
	}
	return append(out, prefix+value)
}

func writeArtifacts(dir string, m Measurement) error {
	reportPath := filepath.Join(dir, "report.json")
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal timing report: %w", err)
	}
	if err := os.WriteFile(reportPath, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write timing report: %w", err)
	}
	markdown, err := os.Create(filepath.Join(dir, "report.md"))
	if err != nil {
		return fmt.Errorf("create Markdown report: %w", err)
	}
	defer markdown.Close()
	if err := WriteMarkdown(markdown, m); err != nil {
		return fmt.Errorf("write Markdown report: %w", err)
	}
	return nil
}
