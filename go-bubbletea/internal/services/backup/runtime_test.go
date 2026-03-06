package backup

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestRuntimeOnOpenStalePolicy(t *testing.T) {
	projectRoot := t.TempDir()
	canonicalDBPath := filepath.Join(projectRoot, ".azedarach", "azedarach.db")
	seedCanonicalDB(t, canonicalDBPath)

	now := time.Date(2026, time.March, 6, 1, 0, 0, 0, time.UTC)
	runtime := NewRuntime(
		projectRoot,
		canonicalDBPath,
		RuntimeConfig{
			Enabled:              true,
			IntervalMinutes:      60,
			WriteCooldownSeconds: 300,
			MaxBackups:           30,
			Directory:            ".azedarach/backups",
		},
		WithClock(func() time.Time { return now }),
	)

	// First open with no prior backup should create one backup.
	runtime.OnOpen()
	assertBackupFileCount(t, filepath.Join(projectRoot, ".azedarach", "backups"), 1)

	// Fresh window should skip.
	now = now.Add(30 * time.Minute)
	runtime.OnOpen()
	assertBackupFileCount(t, filepath.Join(projectRoot, ".azedarach", "backups"), 1)

	// Stale window should create the next backup.
	now = now.Add(31 * time.Minute)
	runtime.OnOpen()
	assertBackupFileCount(t, filepath.Join(projectRoot, ".azedarach", "backups"), 2)
}

func TestRuntimeOnMutationWriteCooldown(t *testing.T) {
	projectRoot := t.TempDir()
	canonicalDBPath := filepath.Join(projectRoot, ".azedarach", "azedarach.db")
	seedCanonicalDB(t, canonicalDBPath)

	now := time.Date(2026, time.March, 6, 2, 0, 0, 0, time.UTC)
	runtime := NewRuntime(
		projectRoot,
		canonicalDBPath,
		RuntimeConfig{
			Enabled:              true,
			IntervalMinutes:      60,
			WriteCooldownSeconds: 300,
			MaxBackups:           30,
			Directory:            ".azedarach/backups",
		},
		WithClock(func() time.Time { return now }),
	)

	runtime.OnMutationSuccess()
	assertBackupFileCount(t, filepath.Join(projectRoot, ".azedarach", "backups"), 1)

	now = now.Add(100 * time.Second)
	runtime.OnMutationSuccess()
	assertBackupFileCount(t, filepath.Join(projectRoot, ".azedarach", "backups"), 1)

	now = now.Add(201 * time.Second)
	runtime.OnMutationSuccess()
	assertBackupFileCount(t, filepath.Join(projectRoot, ".azedarach", "backups"), 2)
}

func TestRuntimeRetentionPrunesOldestBackups(t *testing.T) {
	projectRoot := t.TempDir()
	canonicalDBPath := filepath.Join(projectRoot, ".azedarach", "azedarach.db")
	seedCanonicalDB(t, canonicalDBPath)

	now := time.Date(2026, time.March, 6, 3, 0, 0, 0, time.UTC)
	runtime := NewRuntime(
		projectRoot,
		canonicalDBPath,
		RuntimeConfig{
			Enabled:              true,
			IntervalMinutes:      1,
			WriteCooldownSeconds: 1,
			MaxBackups:           2,
			Directory:            ".azedarach/backups",
		},
		WithClock(func() time.Time { return now }),
	)

	runtime.OnMutationSuccess()
	now = now.Add(1 * time.Second)
	runtime.OnMutationSuccess()
	now = now.Add(1 * time.Second)
	runtime.OnMutationSuccess()
	now = now.Add(1 * time.Second)
	runtime.OnMutationSuccess()

	backupDir := filepath.Join(projectRoot, ".azedarach", "backups")
	assertBackupFileCount(t, backupDir, 2)

	names, err := readDirNames(backupDir)
	if err != nil {
		t.Fatalf("read backup names: %v", err)
	}

	filtered := make([]string, 0, len(names))
	for _, name := range names {
		if _, ok := ParseBackupFilenameTimestamp(name); ok {
			filtered = append(filtered, name)
		}
	}

	sort.Strings(filtered)
	if len(filtered) != 2 {
		t.Fatalf("expected 2 backup files after retention prune, got %d (%v)", len(filtered), filtered)
	}
	if filtered[0] != "issues-20260306T030002Z.db" || filtered[1] != "issues-20260306T030003Z.db" {
		t.Fatalf("unexpected retained backups: %v", filtered)
	}
}

func TestRuntimeFailureNonBlockingAndWarningThrottled(t *testing.T) {
	projectRoot := t.TempDir()
	canonicalDBPath := filepath.Join(projectRoot, ".azedarach", "azedarach.db")
	seedCanonicalDB(t, canonicalDBPath)

	now := time.Date(2026, time.March, 6, 4, 0, 0, 0, time.UTC)
	warnings := make([]string, 0)

	runtime := NewRuntime(
		projectRoot,
		canonicalDBPath,
		RuntimeConfig{
			Enabled:              true,
			IntervalMinutes:      1,
			WriteCooldownSeconds: 300,
			MaxBackups:           30,
			Directory:            ".azedarach/backups",
		},
		WithClock(func() time.Time { return now }),
		WithSnapshotter(func(_, _ string) error {
			return errors.New("simulated snapshot failure")
		}),
		WithWarningHandler(func(message string) {
			warnings = append(warnings, message)
		}),
	)

	// First failure warns.
	runtime.OnOpen()

	// Second failure inside cooldown does not warn again.
	now = now.Add(10 * time.Second)
	runtime.OnOpen()

	// Third failure after cooldown warns again.
	now = now.Add(301 * time.Second)
	runtime.OnOpen()

	if len(warnings) != 2 {
		t.Fatalf("expected throttled warnings count=2, got %d (%v)", len(warnings), warnings)
	}

	for _, warning := range warnings {
		if !strings.Contains(warning, "Local backup attempt failed") {
			t.Fatalf("expected actionable warning, got %q", warning)
		}
	}

	assertBackupFileCount(t, filepath.Join(projectRoot, ".azedarach", "backups"), 0)
}

func seedCanonicalDB(t *testing.T, canonicalDBPath string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(canonicalDBPath), 0o755); err != nil {
		t.Fatalf("mkdir canonical db dir: %v", err)
	}
	if err := os.WriteFile(canonicalDBPath, []byte("canonical-db"), 0o600); err != nil {
		t.Fatalf("write canonical db fixture: %v", err)
	}
}

func assertBackupFileCount(t *testing.T, backupDir string, want int) {
	t.Helper()

	got, err := countBackupFiles(backupDir)
	if err != nil {
		t.Fatalf("count backup files: %v", err)
	}
	if got != want {
		t.Fatalf("backup count = %d, want %d", got, want)
	}
}
