package testtiming

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/gocache"
	"github.com/riordanpawley/azedarach/internal/testisolation"
)

type RunOptions struct {
	Profile                   Profile
	Baseline                  Baseline
	OutputDir                 string
	WorkingDir                string
	CheckBudgets              bool
	PublishValidationEvidence bool
	Now                       func() time.Time
}

func Run(ctx context.Context, opts RunOptions) (Measurement, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.OutputDir == "" {
		return Measurement{}, fmt.Errorf("output directory is required")
	}
	cacheConfig, err := gocache.FromEnvironmentForRepository(ctx, gocache.KindForProfile(opts.Profile.Name), opts.WorkingDir)
	if err != nil {
		return Measurement{}, fmt.Errorf("resolve Go build-cache protocol: %w", err)
	}
	var measurement Measurement
	var runErr error
	lockErr := gocache.WithValidationLock(ctx, cacheConfig, os.Getenv("AZEDARACH_GO_CACHE_AUTO_MAINTAIN") == "1", func(telemetry gocache.Telemetry, err error) error {
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
		TimingBudgetPolicy:  timingBudgetPolicy(opts.CheckBudgets),
		BuildCache:          telemetry,
		ResourceMethod:      "direct-go-command-process-state-v1",
		ProcessLoad:         ProcessLoadEvidence{Method: "ps-process-tree-v2", PeakProcesses: []GoProcess{}, PeakExternalProcesses: []GoProcess{}},
		ValidationLease:     validationLeaseEvidence(),
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
	if opts.PublishValidationEvidence {
		if err := writeValidationLeaseEvidenceFile(measurement, opts.OutputDir); err != nil {
			return measurement, errors.Join(refusal, err)
		}
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
	loadSampler := startProcessLoadSampler(500 * time.Millisecond)
	startedAt := opts.Now().UTC()
	started := time.Now()
	runErr := cmd.Run()
	processLoad := loadSampler.finish()
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
	m := Measurement{Schema: ReportSchema, Profile: opts.Profile.Name, CacheMode: opts.Profile.CachePolicy(), TestResultCacheMode: opts.Profile.CachePolicy(), TimingBudgetPolicy: timingBudgetPolicy(opts.CheckBudgets), BuildCache: cacheTelemetry, ResourceMethod: "direct-go-command-process-state-v1", StartedAt: startedAt, WallSeconds: wall, ProcessLoad: processLoad, ValidationLease: validationLeaseEvidence(), ExitCode: exitCode, Packages: packages, Tests: tests, Failures: failures, InvalidEvents: invalid, RawJSONPath: rawPath, StderrPath: stderrPath, Command: command}
	if cmd.ProcessState != nil {
		m.UserCPUSeconds = cmd.ProcessState.UserTime().Seconds()
		m.SystemCPUSeconds = cmd.ProcessState.SystemTime().Seconds()
		m.PeakRSSBytes = peakRSSBytes(cmd.ProcessState)
	}
	m.Comparison = Compare(m, opts.Baseline)
	if err := writeArtifacts(opts.OutputDir, m); err != nil {
		return m, err
	}
	if opts.PublishValidationEvidence {
		if err := writeValidationLeaseEvidenceFile(m, opts.OutputDir); err != nil {
			return m, err
		}
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

func timingBudgetPolicy(enforced bool) string {
	if enforced {
		return "enforced"
	}
	return "diagnostic-only"
}

func validationLeaseEvidence() ValidationLeaseEvidence {
	requestID := strings.TrimSpace(os.Getenv("AZEDARACH_VALIDATION_REQUEST_ID"))
	return ValidationLeaseEvidence{Held: requestID != "", RequestID: requestID, Class: strings.TrimSpace(os.Getenv("AZEDARACH_VALIDATION_CLASS")), Scope: strings.TrimSpace(os.Getenv("AZEDARACH_VALIDATION_SCOPE")), Purpose: strings.TrimSpace(os.Getenv("AZEDARACH_VALIDATION_PURPOSE")), Execution: strings.TrimSpace(os.Getenv("AZEDARACH_VALIDATION_EXECUTION")), AuthoritativeRequestID: strings.TrimSpace(os.Getenv("AZEDARACH_VALIDATION_AUTHORITATIVE_REQUEST_ID")), Override: strings.TrimSpace(os.Getenv("AZEDARACH_VALIDATION_OVERRIDE")), Profile: strings.TrimSpace(os.Getenv("AZEDARACH_VALIDATION_PROFILE")), SourceRevision: strings.TrimSpace(os.Getenv("AZEDARACH_VALIDATION_SOURCE_REVISION"))}
}

func writeValidationLeaseEvidenceFile(measurement Measurement, outputDir string) error {
	path := strings.TrimSpace(os.Getenv("AZEDARACH_VALIDATION_EVIDENCE_FILE"))
	if path == "" {
		return nil
	}
	reportPath := filepath.Join(outputDir, "report.json")
	evidence := validationLeaseEvidenceFile{Held: measurement.ValidationLease.Held, RequestID: measurement.ValidationLease.RequestID, Class: measurement.ValidationLease.Class, Scope: measurement.ValidationLease.Scope, Purpose: measurement.ValidationLease.Purpose, Execution: measurement.ValidationLease.Execution, AuthoritativeRequestID: measurement.ValidationLease.AuthoritativeRequestID, Override: measurement.ValidationLease.Override, Profile: measurement.ValidationLease.Profile, SourceRevision: measurement.ValidationLease.SourceRevision, Present: true, ReportPath: reportPath, ReportPaths: []string{reportPath}, FailureSummary: FailureSummary(measurement.Failures), OverlapDetected: measurement.ProcessLoad.OverlapDetected, ExternalGoProcesses: measurement.ProcessLoad.MaxExternalGoProcesses}
	if existingData, err := os.ReadFile(path); err == nil {
		if len(strings.TrimSpace(string(existingData))) > 0 {
			var existing validationLeaseEvidenceFile
			if err := json.Unmarshal(existingData, &existing); err != nil {
				return fmt.Errorf("decode accumulated validation lease evidence: %w", err)
			}
			if existing.RequestID != evidence.RequestID || existing.Class != evidence.Class || existing.Scope != evidence.Scope || existing.Purpose != evidence.Purpose || existing.Execution != evidence.Execution || existing.AuthoritativeRequestID != evidence.AuthoritativeRequestID || existing.Override != evidence.Override || existing.Profile != evidence.Profile || existing.SourceRevision != evidence.SourceRevision || existing.Held != evidence.Held {
				return fmt.Errorf("accumulated validation lease evidence identity changed")
			}
			evidence.OverlapDetected = evidence.OverlapDetected || existing.OverlapDetected
			evidence.ExternalGoProcesses = max(evidence.ExternalGoProcesses, existing.ExternalGoProcesses)
			evidence.ReportPaths = appendUniqueValidationReport(existing.ReportPaths, existing.ReportPath, reportPath)
			if evidence.FailureSummary == "" {
				evidence.FailureSummary = existing.FailureSummary
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read accumulated validation lease evidence: %w", err)
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		return fmt.Errorf("marshal validation lease evidence: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write validation lease evidence: %w", err)
	}
	return nil
}

type validationLeaseEvidenceFile struct {
	Held                   bool     `json:"held"`
	RequestID              string   `json:"request_id,omitempty"`
	Class                  string   `json:"class,omitempty"`
	Scope                  string   `json:"scope,omitempty"`
	Purpose                string   `json:"purpose,omitempty"`
	Execution              string   `json:"execution,omitempty"`
	AuthoritativeRequestID string   `json:"authoritative_request_id,omitempty"`
	Override               string   `json:"override,omitempty"`
	Profile                string   `json:"profile,omitempty"`
	SourceRevision         string   `json:"source_revision,omitempty"`
	Present                bool     `json:"present"`
	ReportPath             string   `json:"report_path,omitempty"`
	ReportPaths            []string `json:"report_paths,omitempty"`
	FailureSummary         string   `json:"failure_summary,omitempty"`
	OverlapDetected        bool     `json:"overlap_detected"`
	ExternalGoProcesses    int      `json:"external_go_processes"`
}

// FailureSummary preserves actionable test identity and output when the full
// report lives in a disposable validation worktree. It is deliberately bounded
// because validation evidence is stored inline in the durable request ledger.
func FailureSummary(failures []Failure) string {
	const maxBytes = 32 * 1024
	var summary strings.Builder
	for i, failure := range failures {
		name := strings.TrimSpace(failure.Package)
		if test := strings.TrimSpace(failure.Test); test != "" {
			name += "::" + test
		}
		entry := "FAIL " + name
		if output := strings.TrimSpace(failure.Output); output != "" {
			entry += "\n" + output
		}
		entry += "\n"
		remaining := maxBytes - summary.Len()
		if remaining <= 0 {
			break
		}
		if len(entry) > remaining {
			summary.WriteString(entry[:remaining])
			summary.WriteString("\n[failure summary truncated]\n")
			break
		}
		summary.WriteString(entry)
		if i == len(failures)-1 {
			break
		}
	}
	return strings.TrimSpace(summary.String())
}

func appendUniqueValidationReport(paths []string, candidates ...string) []string {
	out := append([]string(nil), paths...)
	for _, candidate := range candidates {
		if candidate == "" || slices.Contains(out, candidate) {
			continue
		}
		out = append(out, candidate)
	}
	return out
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

// WriteArtifacts publishes a derived aggregate using the same report schema as
// individual samples.
func WriteArtifacts(dir string, m Measurement) error { return writeArtifacts(dir, m) }
