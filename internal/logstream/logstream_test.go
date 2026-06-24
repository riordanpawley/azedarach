package logstream

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadLastMerged_SortsByTimestampAcrossSources(t *testing.T) {
	tmp := t.TempDir()
	daemonPath := filepath.Join(tmp, "azd.log")
	tuiPath := filepath.Join(tmp, "az-tui.log")

	if err := os.WriteFile(daemonPath, []byte("2026/04/01 16:50:04 INFO daemon started\n"), 0o644); err != nil {
		t.Fatalf("write daemon log: %v", err)
	}
	if err := os.WriteFile(tuiPath, []byte("time=2026-04-01T05:49:59Z level=INFO msg=\"tui first\"\n"), 0o644); err != nil {
		t.Fatalf("write tui log: %v", err)
	}

	entries, err := ReadLastMerged([]SourceSpec{
		{Name: "daemon", Path: daemonPath},
		{Name: "tui", Path: tuiPath},
	}, 10)
	if err != nil {
		t.Fatalf("ReadLastMerged() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("ReadLastMerged() len = %d, want 2", len(entries))
	}
	if entries[0].Source != "tui" {
		t.Fatalf("first source = %q, want tui", entries[0].Source)
	}
	if entries[1].Source != "daemon" {
		t.Fatalf("second source = %q, want daemon", entries[1].Source)
	}
}

func TestReadLastLogLines_ReturnsTailFromLargeLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "azd.log")
	var builder strings.Builder
	builder.WriteString(strings.Repeat("x", tailReadChunkBytes*2))
	for _, line := range []string{"old", "keep-1", "keep-2", "keep-3"} {
		builder.WriteString("\n")
		builder.WriteString(line)
	}
	builder.WriteString("\n")
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	lines, err := readLastLogLines(path, 3)
	if err != nil {
		t.Fatalf("readLastLogLines() error = %v", err)
	}
	want := []string{"keep-1", "keep-2", "keep-3"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("readLastLogLines() = %#v, want %#v", lines, want)
	}
}

func TestReadLastLogLines_DropsPartialLineAtTailReadBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "azd.log")
	var builder strings.Builder
	builder.WriteString(strings.Repeat("x", maxTailReadBytes+tailReadChunkBytes))
	builder.WriteString("\nkeep-1\nkeep-2\n")
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	lines, err := readLastLogLines(path, 10)
	if err != nil {
		t.Fatalf("readLastLogLines() error = %v", err)
	}
	want := []string{"keep-1", "keep-2"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("readLastLogLines() = %#v, want %#v", lines, want)
	}
}

func TestFollow_ReadsReplacementAfterRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "az-tui.log")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("write initial log: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entries := make(chan Entry, 1)
	errs := make(chan error, 1)
	go func() {
		errs <- Follow(ctx, []SourceSpec{{Name: "tui", Path: path}}, 10*time.Millisecond, func(entry Entry) error {
			entries <- entry
			cancel()
			return nil
		})
	}()

	time.Sleep(20 * time.Millisecond)
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatalf("rename rotated log: %v", err)
	}
	if err := os.WriteFile(path, []byte("new\n"), 0o644); err != nil {
		t.Fatalf("write replacement log: %v", err)
	}

	select {
	case entry := <-entries:
		if entry.RawLine != "new" {
			t.Fatalf("followed line = %q, want replacement line", entry.RawLine)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for replacement log entry")
	}
	select {
	case err := <-errs:
		if err != nil {
			t.Fatalf("Follow() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Follow to exit")
	}
}

func TestFormatLine_NormalizesTimestampAndPrefixesSource(t *testing.T) {
	loc := time.FixedZone("AEST", 10*60*60)
	line := "time=2026-04-01T06:50:15Z level=INFO msg=\"hello\""
	formatted := FormatLine("tui", line, loc)
	if !strings.HasPrefix(formatted, "[tui] 2026-04-01 16:50:15 AEST") {
		t.Fatalf("FormatLine() = %q, want normalized source+timestamp prefix", formatted)
	}
	if strings.Contains(formatted, "time=2026-04-01T06:50:15Z") {
		t.Fatalf("FormatLine() = %q, want raw time field removed from body", formatted)
	}
}
