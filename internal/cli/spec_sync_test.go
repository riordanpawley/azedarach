package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRunSpecMarkdownSyncDeterministicRerun(t *testing.T) {
	repoDir := t.TempDir()

	first, err := RunSpecMarkdownSync(repoDir, false)
	if err != nil {
		t.Fatalf("RunSpecMarkdownSync(first) error = %v", err)
	}
	if !first.Changed {
		t.Fatalf("first run should report changes: %+v", first)
	}
	if len(first.Files) != 2 {
		t.Fatalf("first run files = %d, want 2", len(first.Files))
	}
	if first.Files[0].Path != "docs/spec/README.md" || first.Files[1].Path != "docs/spec/az-spec-phase1.md" {
		t.Fatalf("unexpected file ordering: %+v", first.Files)
	}
	for _, file := range first.Files {
		if file.Status != "created" {
			t.Fatalf("first run status for %s = %q, want created", file.Path, file.Status)
		}
		if _, err := os.Stat(filepath.Join(repoDir, filepath.FromSlash(file.Path))); err != nil {
			t.Fatalf("expected synced file %s: %v", file.Path, err)
		}
	}

	second, err := RunSpecMarkdownSync(repoDir, false)
	if err != nil {
		t.Fatalf("RunSpecMarkdownSync(second) error = %v", err)
	}
	if second.Changed {
		t.Fatalf("second run should be stable: %+v", second)
	}
	for _, file := range second.Files {
		if file.Status != "ok" {
			t.Fatalf("second run status for %s = %q, want ok", file.Path, file.Status)
		}
	}
}

func TestRunSpecMarkdownSyncCheckModeDetectsDrift(t *testing.T) {
	repoDir := t.TempDir()
	if _, err := RunSpecMarkdownSync(repoDir, false); err != nil {
		t.Fatalf("seed sync error = %v", err)
	}

	clean, err := RunSpecMarkdownSync(repoDir, true)
	if err != nil {
		t.Fatalf("clean check error = %v", err)
	}
	if clean.Changed {
		t.Fatalf("clean check should not report drift: %+v", clean)
	}

	readmePath := filepath.Join(repoDir, "docs", "spec", "README.md")
	if err := os.WriteFile(readmePath, []byte("manual drift\n"), 0o644); err != nil {
		t.Fatalf("write drift file: %v", err)
	}

	drift, err := RunSpecMarkdownSync(repoDir, true)
	if !errors.Is(err, ErrSpecMarkdownDrift) {
		t.Fatalf("drift check error = %v, want ErrSpecMarkdownDrift", err)
	}
	if !drift.Changed {
		t.Fatalf("drift check should report changes: %+v", drift)
	}
	if drift.Files[0].Status != "drift" {
		t.Fatalf("README status = %q, want drift", drift.Files[0].Status)
	}
	if drift.Files[1].Status != "ok" {
		t.Fatalf("phase1 status = %q, want ok", drift.Files[1].Status)
	}
}
