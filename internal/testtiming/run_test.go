package testtiming

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/riordanpawley/azedarach/internal/dbpathguard"
)

func TestRunWritesCompleteArtifactsBeforeReturningTestFailure(t *testing.T) {
	module := t.TempDir()
	configureTestCacheFamily(t, module)
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
	assert.Equal(t, "direct-go-command-process-state-v1", measurement.ResourceMethod)
	assert.Positive(t, measurement.PeakRSSBytes)
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

func TestRunForcesIsolatedHomeConfigAndDatabaseRoots(t *testing.T) {
	originalHome := t.TempDir()
	registered := filepath.Join(t.TempDir(), "registered")
	require.NoError(t, os.MkdirAll(filepath.Join(originalHome, ".config", "azedarach"), 0o755))
	registry, err := json.Marshal(map[string]any{"projects": []map[string]string{{"name": "production", "path": registered}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(originalHome, ".config", "azedarach", "projects.json"), registry, 0o600))
	t.Setenv("HOME", originalHome)
	t.Setenv("AZEDARACH_USER_DB_PATH", "")
	t.Setenv("AZEDARACH_DB_PATH", "")
	t.Setenv("AZEDARACH_REFUSE_DB_PATHS", "")
	t.Setenv("AZEDARACH_REFUSE_DB_PATH", "")

	module := t.TempDir()
	configureTestCacheFamily(t, module)
	require.NoError(t, os.WriteFile(filepath.Join(module, "go.mod"), []byte("module example.test/isolation\n\ngo 1.24.2\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(module, "isolation_test.go"), []byte(`package isolation
import (
  "encoding/json"
  "os"
  "path/filepath"
  "strings"
  "testing"
)
func TestEnvironment(t *testing.T) {
  root := os.Getenv("AZEDARACH_TEST_ISOLATION_ROOT")
  if root == "" || os.Getenv("HOME") == os.Getenv("ORIGINAL_HOME_FOR_TEST") { t.Fatal("HOME not isolated") }
  for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "AZEDARACH_USER_DB_PATH", "AZEDARACH_DB_PATH"} {
    value := os.Getenv(key)
    rel, err := filepath.Rel(root, value)
    if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) { t.Fatalf("%s escaped isolation root: %s", key, value) }
  }
  var refused []string
  if err := json.Unmarshal([]byte(os.Getenv("AZEDARACH_REFUSE_DB_PATHS")), &refused); err != nil { t.Fatal(err) }
  want := filepath.Join(os.Getenv("REGISTERED_ROOT_FOR_TEST"), ".azedarach", "azedarach.db")
  for _, path := range refused { if path == want { return } }
  t.Fatalf("registered original missing from refusal set: %v", refused)
}
`), 0o644))
	t.Setenv("ORIGINAL_HOME_FOR_TEST", originalHome)
	canonicalRegistered, err := dbpathguard.Canonical(registered)
	require.NoError(t, err)
	t.Setenv("REGISTERED_ROOT_FOR_TEST", canonicalRegistered)
	output := filepath.Join(t.TempDir(), "artifacts")
	baseline := Baseline{Schema: BaselineSchema, RecordedAt: "2026-07-13", Profiles: map[string]BaselineProfile{}, Budgets: Budgets{RegressionFactor: 1.25, MaxWallSeconds: map[string]float64{"fixture": 60}, DefaultPackageSeconds: 60, PackageSeconds: map[string]float64{}, DefaultTestSeconds: 30, TestSeconds: map[string]float64{}}}
	profile := Profile{Name: "fixture", Packages: []string{"./..."}, GoTestArgs: []string{"-json", "-count=1"}}

	measurement, err := Run(context.Background(), RunOptions{Profile: profile, Baseline: baseline, OutputDir: output, WorkingDir: module, CheckBudgets: true})
	diagnostics, _ := os.ReadFile(measurement.StderrPath)
	rawDiagnostics, _ := os.ReadFile(measurement.RawJSONPath)
	require.NoError(t, err, "failures: %+v; stderr: %s; raw: %s", measurement.Failures, diagnostics, rawDiagnostics)
	assert.Zero(t, measurement.ExitCode)
}

func configureTestCacheFamily(t *testing.T, workingDir string) {
	t.Helper()
	t.Setenv("AZEDARACH_GO_CACHE_ROOT", filepath.Join(workingDir, ".azedarach", "go"))
	t.Setenv("AZEDARACH_GOCACHE", "")
	t.Setenv("AZEDARACH_GO_CACHE_OWNER", "issue-test")
	t.Setenv("AZEDARACH_GO_CACHE_SOFT_LIMIT_BYTES", "104857600")
	t.Setenv("AZEDARACH_GO_CACHE_HARD_LIMIT_BYTES", "209715200")
}

func TestRunWritesMachineReadableArtifactsWhenBuildCacheHardLimitRefuses(t *testing.T) {
	module := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(module, "go.mod"), []byte("module example.test/refusal\n\ngo 1.24.2\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(module, "refusal_test.go"), []byte("package refusal\nimport \"testing\"\nfunc TestMustNotRun(t *testing.T) { t.Fatal(\"command ran\") }\n"), 0o644))
	cacheRoot := filepath.Join(module, ".azedarach", "go")
	t.Setenv("AZEDARACH_GO_CACHE_ROOT", cacheRoot)
	t.Setenv("AZEDARACH_GOCACHE", "")
	t.Setenv("AZEDARACH_GO_CACHE_OWNER", "issue-dhc")
	t.Setenv("AZEDARACH_GO_CACHE_SOFT_LIMIT_BYTES", "4")
	t.Setenv("AZEDARACH_GO_CACHE_HARD_LIMIT_BYTES", "8")
	cachePath := filepath.Join(cacheRoot, "caches", "v1", "normal", "issue-dhc")
	require.NoError(t, os.MkdirAll(cachePath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cachePath, "oversized"), []byte("0123456789"), 0o644))

	output := filepath.Join(t.TempDir(), "artifacts")
	baseline := Baseline{RecordedAt: "2026-07-13"}
	profile := Profile{Name: "fixture", Packages: []string{"./..."}, GoTestArgs: []string{"-json", "-count=1"}}

	measurement, err := Run(context.Background(), RunOptions{Profile: profile, Baseline: baseline, OutputDir: output, WorkingDir: module, CheckBudgets: true})
	require.ErrorContains(t, err, "above hard limit")
	assert.Equal(t, "refused-hard-limit", measurement.BuildCache.Decision)
	assert.Equal(t, 1, measurement.ExitCode)
	for _, name := range []string{"events.jsonl", "stderr.txt", "report.json", "report.md"} {
		_, statErr := os.Stat(filepath.Join(output, name))
		require.NoError(t, statErr, name)
	}
	raw, readErr := os.ReadFile(filepath.Join(output, "events.jsonl"))
	require.NoError(t, readErr)
	assert.Empty(t, raw, "refused validation must not execute go test")
}
