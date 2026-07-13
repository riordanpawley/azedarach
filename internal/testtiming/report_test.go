package testtiming

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRepresentativeMarkdownReport(t *testing.T) {
	m := Measurement{Schema: ReportSchema, Profile: "cold", StartedAt: time.Date(2026, 7, 13, 1, 2, 3, 0, time.UTC), WallSeconds: 12.5, UserCPUSeconds: 8.2, SystemCPUSeconds: 1.1, ExitCode: 1, Packages: []Duration{{Name: "example/slow", Seconds: 9, Action: "fail"}, {Name: "example/fast", Seconds: 1, Action: "pass"}}, Tests: []Duration{{Name: "example/slow::TestPathological", Seconds: 8.5, Action: "fail"}}, Failures: []Failure{{Package: "example/slow", Test: "TestPathological", Output: "sentinel failure"}}, RawJSONPath: ".tmp/test-timing/cold/events.jsonl", StderrPath: ".tmp/test-timing/cold/stderr.txt", Command: []string{"go", "test", "-json", "-count=1", "./..."}, Comparison: Comparison{BaselineRecordedAt: "2026-07-13", WallDelta: &Delta{Name: "wall", CurrentSeconds: 12.5, BaselineSeconds: 10, Percent: 25}, PackageDeltas: []Delta{{Name: "example/slow", CurrentSeconds: 9, BaselineSeconds: 8, Percent: 12.5}}, Violations: []Violation{{Kind: "test", Name: "example/slow::TestPathological", ActualSeconds: 8.5, BudgetSeconds: 8, BudgetSource: "default test budget"}}}}
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
