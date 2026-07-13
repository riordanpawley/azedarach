package testtiming

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

func WriteMarkdown(w io.Writer, m Measurement) error {
	report := markdownWriter{w: w}
	write := report.printf
	write("# Test timing report: %s\n\n", m.Profile)
	write("- Started: %s\n", m.StartedAt.UTC().Format("2006-01-02T15:04:05Z"))
	write("- Command: `%s`\n", strings.Join(m.Command, " "))
	write("- Cache: %s\n", m.CacheMode)
	write("- Resource measurement: `%s` (direct `go` command process; descendant test-binary resources are not aggregated)\n", m.ResourceMethod)
	write("- Result: exit %d; %.2fs wall; %.2fs user CPU; %.2fs system CPU; %.1f MiB peak RSS\n", m.ExitCode, m.WallSeconds, m.UserCPUSeconds, m.SystemCPUSeconds, float64(m.PeakRSSBytes)/(1024*1024))
	write("- Events: %d packages; %d tests; %d failures; %d invalid lines\n", len(m.Packages), len(m.Tests), len(m.Failures), m.InvalidEvents)
	write("- Raw events: `%s`; stderr: `%s`\n", filepath.ToSlash(m.RawJSONPath), filepath.ToSlash(m.StderrPath))
	if m.Comparison.WallDelta != nil {
		write("- Baseline (%s): %.2fs (%+.1f%%)\n", m.Comparison.BaselineRecordedAt, m.Comparison.WallDelta.BaselineSeconds, m.Comparison.WallDelta.Percent)
	}
	if m.Comparison.UserCPUDelta != nil && m.Comparison.SystemCPUDelta != nil {
		write("- CPU baseline: %.2fs user (%+.1f%%); %.2fs system (%+.1f%%)\n", m.Comparison.UserCPUDelta.BaselineSeconds, m.Comparison.UserCPUDelta.Percent, m.Comparison.SystemCPUDelta.BaselineSeconds, m.Comparison.SystemCPUDelta.Percent)
	}
	if m.Comparison.PeakRSSDelta != nil {
		write("- Peak RSS baseline: %.1f MiB (%+.1f%%)\n", float64(m.Comparison.PeakRSSDelta.BaselineBytes)/(1024*1024), m.Comparison.PeakRSSDelta.Percent)
	}
	write("- Budget violations: %d\n\n", len(m.Comparison.Violations))
	write("## Slowest packages\n\n| Package | Seconds | Result | Baseline delta |\n|---|---:|---|---:|\n")
	for _, item := range m.Packages {
		delta := "—"
		result := item.Action
		if item.Cached {
			result += " (cached)"
		}
		for _, d := range m.Comparison.PackageDeltas {
			if d.Name == item.Name {
				delta = fmt.Sprintf("%+.1f%%", d.Percent)
				break
			}
		}
		write("| `%s` | %.2f | %s | %s |\n", item.Name, item.Seconds, result, delta)
	}
	write("\n## Slowest tests\n\n| Test | Seconds | Result |\n|---|---:|---|\n")
	for _, item := range m.Tests {
		result := item.Action
		if item.Cached {
			result += " (cached)"
		}
		write("| `%s` | %.2f | %s |\n", item.Name, item.Seconds, result)
	}
	write("\n## Failures\n\n")
	if len(m.Failures) == 0 {
		write("None.\n")
	}
	for _, failure := range m.Failures {
		name := failure.Package
		if failure.Test != "" {
			name += "::" + failure.Test
		}
		write("### `%s`\n\n```text\n%s\n```\n\n", name, failure.Output)
	}
	write("## Budget violations\n\n")
	if len(m.Comparison.Violations) == 0 {
		write("None.\n")
	}
	for _, v := range m.Comparison.Violations {
		write("- %s `%s`: %.2fs > %.2fs (%s)\n", v.Kind, v.Name, v.ActualSeconds, v.BudgetSeconds, v.BudgetSource)
	}
	return report.err
}

type markdownWriter struct {
	w   io.Writer
	err error
}

func (w *markdownWriter) printf(format string, args ...any) {
	if w.err != nil {
		return
	}
	_, w.err = fmt.Fprintf(w.w, format, args...)
}
