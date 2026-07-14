// Package testtiming implements the canonical machine-readable Go test timing
// runner and its baseline/budget comparisons.
package testtiming

import (
	"time"

	"github.com/riordanpawley/azedarach/internal/gocache"
)

const (
	ReportSchema   = "azedarach.test_timing_report.v3"
	BaselineSchema = "azedarach.test_timing_baseline.v1"
)

type Profile struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Packages    []string `json:"packages"`
	GoTestArgs  []string `json:"go_test_args"`
	CleanCache  bool     `json:"clean_cache"`
}

type Duration struct {
	Name    string  `json:"name"`
	Seconds float64 `json:"seconds"`
	Action  string  `json:"action,omitempty"`
	Cached  bool    `json:"cached,omitempty"`
}

type Failure struct {
	Package string `json:"package"`
	Test    string `json:"test,omitempty"`
	Output  string `json:"output,omitempty"`
}

type Measurement struct {
	Schema              string                  `json:"schema"`
	Profile             string                  `json:"profile"`
	CacheMode           string                  `json:"cache_mode"`
	TestResultCacheMode string                  `json:"test_result_cache_mode"`
	BuildCache          gocache.Telemetry       `json:"build_cache"`
	ResourceMethod      string                  `json:"resource_measurement"`
	StartedAt           time.Time               `json:"started_at"`
	WallSeconds         float64                 `json:"wall_seconds"`
	UserCPUSeconds      float64                 `json:"user_cpu_seconds"`
	SystemCPUSeconds    float64                 `json:"system_cpu_seconds"`
	PeakRSSBytes        int64                   `json:"peak_rss_bytes"`
	ProcessLoad         ProcessLoadEvidence     `json:"process_load"`
	ValidationLease     ValidationLeaseEvidence `json:"validation_lease"`
	ExitCode            int                     `json:"exit_code"`
	Packages            []Duration              `json:"packages"`
	Tests               []Duration              `json:"tests"`
	Failures            []Failure               `json:"failures"`
	InvalidEvents       int                     `json:"invalid_events"`
	RawJSONPath         string                  `json:"raw_json_path"`
	StderrPath          string                  `json:"stderr_path"`
	Command             []string                `json:"command"`
	Comparison          Comparison              `json:"comparison"`
}

type ValidationLeaseEvidence struct {
	Held           bool   `json:"held"`
	RequestID      string `json:"request_id,omitempty"`
	Class          string `json:"class,omitempty"`
	Profile        string `json:"profile,omitempty"`
	SourceRevision string `json:"source_revision,omitempty"`
}

type BaselineProfile struct {
	WallSeconds      float64    `json:"wall_seconds"`
	UserCPUSeconds   float64    `json:"user_cpu_seconds,omitempty"`
	SystemCPUSeconds float64    `json:"system_cpu_seconds,omitempty"`
	PeakRSSBytes     int64      `json:"peak_rss_bytes,omitempty"`
	ResourceMethod   string     `json:"resource_measurement,omitempty"`
	Packages         []Duration `json:"packages,omitempty"`
}

type Budgets struct {
	RegressionFactor      float64            `json:"regression_factor"`
	MaxWallSeconds        map[string]float64 `json:"max_wall_seconds"`
	DefaultPackageSeconds float64            `json:"default_package_seconds"`
	PackageSeconds        map[string]float64 `json:"package_seconds"`
	DefaultTestSeconds    float64            `json:"default_test_seconds"`
	TestSeconds           map[string]float64 `json:"test_seconds"`
}

type Baseline struct {
	Schema     string                     `json:"schema"`
	RecordedAt string                     `json:"recorded_at"`
	Source     string                     `json:"source"`
	Profiles   map[string]BaselineProfile `json:"profiles"`
	Budgets    Budgets                    `json:"budgets"`
}

type Delta struct {
	Name            string  `json:"name"`
	CurrentSeconds  float64 `json:"current_seconds"`
	BaselineSeconds float64 `json:"baseline_seconds"`
	Percent         float64 `json:"percent"`
}

type ByteDelta struct {
	Name          string  `json:"name"`
	CurrentBytes  int64   `json:"current_bytes"`
	BaselineBytes int64   `json:"baseline_bytes"`
	Percent       float64 `json:"percent"`
}

type Violation struct {
	Kind          string  `json:"kind"`
	Name          string  `json:"name"`
	ActualSeconds float64 `json:"actual_seconds"`
	BudgetSeconds float64 `json:"budget_seconds"`
	BudgetSource  string  `json:"budget_source"`
}

type Comparison struct {
	BaselineRecordedAt string      `json:"baseline_recorded_at"`
	WallDelta          *Delta      `json:"wall_delta,omitempty"`
	UserCPUDelta       *Delta      `json:"user_cpu_delta,omitempty"`
	SystemCPUDelta     *Delta      `json:"system_cpu_delta,omitempty"`
	PeakRSSDelta       *ByteDelta  `json:"peak_rss_delta,omitempty"`
	PackageDeltas      []Delta     `json:"package_deltas"`
	Violations         []Violation `json:"violations"`
}
