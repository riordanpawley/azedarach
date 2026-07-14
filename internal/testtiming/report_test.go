package testtiming

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/riordanpawley/azedarach/internal/gocache"
)

func TestRepresentativeMarkdownReport(t *testing.T) {
	m := Measurement{Schema: ReportSchema, Profile: "cold", CacheMode: "cleared-and-bypassed", TestResultCacheMode: "cleared-and-bypassed", BuildCache: gocache.Telemetry{Namespace: "normal/issue-dhc", Policy: "retained-build-cache", Before: gocache.Stats{Bytes: 100, Files: 2}, After: gocache.Stats{Bytes: 140, Files: 3}, DeltaBytes: 40, DeltaFiles: 1, FamilyBytes: 500, Decision: "within-limits"}, StartedAt: time.Date(2026, 7, 13, 1, 2, 3, 0, time.UTC), WallSeconds: 12.5, UserCPUSeconds: 8.2, SystemCPUSeconds: 1.1, PeakRSSBytes: 64 * 1024 * 1024, ProcessLoad: ProcessLoadEvidence{Method: "ps-pid-ppid-comm-v1", SampleInterval: "500ms", Samples: 25, MaxGoProcesses: 4}, ValidationLease: ValidationLeaseEvidence{Held: true, RequestID: "req-1", Class: "aggregate", Profile: "cold"}, ExitCode: 1, Packages: []Duration{{Name: "example/slow", Seconds: 9, Action: "fail"}, {Name: "example/fast", Seconds: 1, Action: "pass"}}, Tests: []Duration{{Name: "example/slow::TestPathological", Seconds: 8.5, Action: "fail"}}, Failures: []Failure{{Package: "example/slow", Test: "TestPathological", Output: "sentinel failure"}}, RawJSONPath: ".tmp/test-timing/cold/events.jsonl", StderrPath: ".tmp/test-timing/cold/stderr.txt", Command: []string{"go", "test", "-json", "-count=1", "./..."}, Comparison: Comparison{BaselineRecordedAt: "2026-07-13", WallDelta: &Delta{Name: "wall", CurrentSeconds: 12.5, BaselineSeconds: 10, Percent: 25}, UserCPUDelta: &Delta{Name: "user CPU", BaselineSeconds: 10, Percent: -18}, SystemCPUDelta: &Delta{Name: "system CPU", BaselineSeconds: 2, Percent: -45}, PeakRSSDelta: &ByteDelta{Name: "peak RSS", BaselineBytes: 128 * 1024 * 1024, Percent: -50}, PackageDeltas: []Delta{{Name: "example/slow", CurrentSeconds: 9, BaselineSeconds: 8, Percent: 12.5}}, Violations: []Violation{{Kind: "test", Name: "example/slow::TestPathological", ActualSeconds: 8.5, BudgetSeconds: 8, BudgetSource: "default test budget"}}}}
	m.ResourceMethod = "direct-go-command-process-state-v1"
	var got bytes.Buffer
	require.NoError(t, WriteMarkdown(&got, m))
	want, err := os.ReadFile(filepath.Join("testdata", "representative-report.golden.md"))
	require.NoError(t, err)
	require.Equal(t, string(want), got.String())
}

func TestWriteMarkdownPropagatesWriterErrors(t *testing.T) {
	want := errors.New("disk full")
	err := WriteMarkdown(errorWriter{err: want}, Measurement{Profile: "focused"})
	require.ErrorIs(t, err, want)
}

type errorWriter struct{ err error }

func (w errorWriter) Write([]byte) (int, error) { return 0, w.err }
