package logstream

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadLastMerged_SortsByTimestampAcrossSources(t *testing.T) {
	tmp := t.TempDir()
	daemonPath := filepath.Join(tmp, "daemon.log")
	tuiPath := filepath.Join(tmp, "az.log")

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
