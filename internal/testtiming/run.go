package testtiming

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

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
	cmd.Env = isolation.Environ(os.Environ())
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
	m := Measurement{Schema: ReportSchema, Profile: opts.Profile.Name, CacheMode: opts.Profile.CachePolicy(), ResourceMethod: "direct-go-command-process-state-v1", StartedAt: startedAt, WallSeconds: wall, ExitCode: exitCode, Packages: packages, Tests: tests, Failures: failures, InvalidEvents: invalid, RawJSONPath: rawPath, StderrPath: stderrPath, Command: command}
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
