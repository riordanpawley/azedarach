package tmuxselector

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
)

func TestDetailOpenSocketCandidatesPreferLiveScopedSocket(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "xdg-runtime"))
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")

	projectPath := filepath.Join(t.TempDir(), "repo")
	scoped := config.ScopedDaemonSocketPath(projectPath)
	if err := os.MkdirAll(filepath.Dir(scoped), 0o755); err != nil {
		t.Fatalf("mkdir scoped runtime: %v", err)
	}
	if err := os.WriteFile(scoped, []byte("socket placeholder"), 0o644); err != nil {
		t.Fatalf("write scoped socket placeholder: %v", err)
	}

	got := detailOpenSocketCandidates(projectPath)
	want := []string{scoped, config.GlobalDaemonSocketPath()}
	if len(got) != len(want) {
		t.Fatalf("socket candidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("socket candidates = %v, want %v", got, want)
		}
	}
}

func TestDetailOpenSocketCandidatesUseDefaultWhenScopedMissing(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "xdg-runtime"))
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")

	projectPath := filepath.Join(t.TempDir(), "repo")
	got := detailOpenSocketCandidates(projectPath)
	want := []string{config.GlobalDaemonSocketPath()}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("socket candidates = %v, want %v", got, want)
	}
}
