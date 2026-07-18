package testtiming

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

const (
	ControlledTimingSamples   = 3
	ControlledTimingRunner    = "azedarach-timing-v1"
	ControlledTimingToolchain = "go1.25.7"
	ControlledTimingResources = "8-vcpu-16-gib-exclusive"
)

// ValidateControlledEnvironment refuses timing authority unless CI explicitly
// attests the controlled runner contract. These markers are intentionally
// separate from ordinary CI=true so local and incidental CI runs cannot claim
// performance authority.
func ValidateControlledEnvironment(getenv func(string) string, samples int) error {
	required := map[string]string{
		"CI":                           "true",
		"AZEDARACH_TIMING_CONTROLLED":  "1",
		"AZEDARACH_TIMING_CACHE_STATE": "clean-per-sample",
		"AZEDARACH_TIMING_RUNNER":      ControlledTimingRunner,
		"AZEDARACH_TIMING_TOOLCHAIN":   ControlledTimingToolchain,
		"AZEDARACH_TIMING_RESOURCES":   ControlledTimingResources,
		"AZEDARACH_VALIDATION_CLASS":   "aggregate",
		"AZEDARACH_VALIDATION_PROFILE": "test-ci-timing",
	}
	for key, want := range required {
		if got := strings.TrimSpace(getenv(key)); got != want {
			return fmt.Errorf("controlled CI timing requires %s=%q (got %q)", key, want, got)
		}
	}
	for _, key := range []string{"AZEDARACH_VALIDATION_REQUEST_ID"} {
		if strings.TrimSpace(getenv(key)) == "" {
			return fmt.Errorf("controlled CI timing requires non-empty %s", key)
		}
	}
	if samples < ControlledTimingSamples {
		return fmt.Errorf("controlled CI timing requires at least %d samples (got %d)", ControlledTimingSamples, samples)
	}
	return nil
}

func ControlledEnvironment(samples int) error {
	return ValidateControlledEnvironment(os.Getenv, samples)
}

// AggregateMedian uses the per-metric median so one scheduler outlier cannot
// decide the timing gate. All sample diagnostics remain in their own reports.
func AggregateMedian(samples []Measurement, baseline Baseline) (Measurement, error) {
	if len(samples) < ControlledTimingSamples {
		return Measurement{}, fmt.Errorf("aggregate requires at least %d samples", ControlledTimingSamples)
	}
	for i, sample := range samples {
		if !sample.ValidationLease.Held || sample.ValidationLease.Class != "aggregate" || sample.ValidationLease.Profile != "test-ci-timing" {
			return Measurement{}, fmt.Errorf("sample %d lacks the controlled aggregate validation lease", i+1)
		}
		if sample.ProcessLoad.OverlapDetected || sample.ProcessLoad.MaxExternalGoProcesses > 0 {
			return Measurement{}, fmt.Errorf("sample %d observed concurrent external Go validation", i+1)
		}
		if sample.TestResultCacheMode != "cleared-and-bypassed" {
			return Measurement{}, fmt.Errorf("sample %d used test-result cache mode %q", i+1, sample.TestResultCacheMode)
		}
	}
	agg := samples[0]
	agg.Profile = "ci-timing"
	agg.TimingBudgetPolicy = "controlled-median-enforced"
	agg.WallSeconds = median(values(samples, func(m Measurement) float64 { return m.WallSeconds }))
	agg.UserCPUSeconds = median(values(samples, func(m Measurement) float64 { return m.UserCPUSeconds }))
	agg.SystemCPUSeconds = median(values(samples, func(m Measurement) float64 { return m.SystemCPUSeconds }))
	agg.Packages = medianDurations(samples, func(m Measurement) []Duration { return m.Packages })
	agg.Tests = medianDurations(samples, func(m Measurement) []Duration { return m.Tests })
	agg.Comparison = Compare(agg, baseline)
	return agg, nil
}

func values(samples []Measurement, get func(Measurement) float64) []float64 {
	out := make([]float64, len(samples))
	for i, sample := range samples {
		out[i] = get(sample)
	}
	return out
}

func median(values []float64) float64 {
	slices.Sort(values)
	return values[len(values)/2]
}

func medianDurations(samples []Measurement, get func(Measurement) []Duration) []Duration {
	byName := map[string][]float64{}
	prototype := map[string]Duration{}
	for _, sample := range samples {
		for _, d := range get(sample) {
			byName[d.Name] = append(byName[d.Name], d.Seconds)
			prototype[d.Name] = d
		}
	}
	out := make([]Duration, 0, len(byName))
	for name, vals := range byName {
		if len(vals) != len(samples) {
			continue
		}
		d := prototype[name]
		d.Seconds = median(vals)
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b Duration) int {
		if a.Seconds > b.Seconds {
			return -1
		}
		if a.Seconds < b.Seconds {
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}
