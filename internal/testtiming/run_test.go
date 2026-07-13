package testtiming

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunWritesCompleteArtifactsBeforeReturningTestFailure(t *testing.T) {
	module := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(module, "go.mod"), []byte("module example.test/failures\n\ngo 1.24.2\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(module, "failure_test.go"), []byte(`package failures
import "testing"
func TestFirst(t *testing.T) { t.Error("first sentinel") }
func TestSecond(t *testing.T) { t.Error("second sentinel") }
`), 0o644))
	output := filepath.Join(t.TempDir(), "artifacts")
	baseline := Baseline{Schema: BaselineSchema, RecordedAt: "2026-07-13", Profiles: map[string]BaselineProfile{}, Budgets: Budgets{RegressionFactor: 1.25, MaxWallSeconds: map[string]float64{"fixture": 60}, DefaultPackageSeconds: 60, PackageSeconds: map[string]float64{}, DefaultTestSeconds: 30, TestSeconds: map[string]float64{}}}
	profile := Profile{Name: "fixture", Packages: []string{"./..."}, GoTestArgs: []string{"-json", "-count=1"}}

	measurement, err := Run(context.Background(), RunOptions{Profile: profile, Baseline: baseline, OutputDir: output, WorkingDir: module, CheckBudgets: true, Now: func() time.Time { return time.Date(2026, 7, 13, 1, 2, 3, 0, time.UTC) }})
	require.ErrorContains(t, err, "test command exited")
	assert.Equal(t, 1, measurement.ExitCode)
	assert.Len(t, measurement.Failures, 3, "two failed tests plus their failed package must all be retained")
	assert.Empty(t, measurement.Comparison.Violations)
	for _, name := range []string{"events.jsonl", "stderr.txt", "report.json", "report.md"} {
		_, statErr := os.Stat(filepath.Join(output, name))
		require.NoError(t, statErr, name)
	}
	raw, err := os.ReadFile(filepath.Join(output, "events.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "first sentinel")
	assert.Contains(t, string(raw), "second sentinel")
	report, err := os.ReadFile(filepath.Join(output, "report.md"))
	require.NoError(t, err)
	assert.True(t, strings.Index(string(report), "TestFirst") < strings.Index(string(report), "TestSecond"), "equal-duration failures have deterministic name ordering")
}
