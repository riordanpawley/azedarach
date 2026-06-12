package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
)

func TestNewScopedRuntimeUsesWorktreeRuntimeStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("PATH", "")

	base := t.TempDir()
	repo := filepath.Join(base, "azedarach")
	worktree := filepath.Join(base, "azedarach-cmg")
	nested := filepath.Join(worktree, "internal", "daemon")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "azedarach-cmg"), 0o755); err != nil {
		t.Fatalf("mkdir repo git metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested worktree path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "azedarach-cmg")+"\n"), 0o644); err != nil {
		t.Fatalf("write worktree gitdir: %v", err)
	}

	d := New(Config{
		RepoDir: nested,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(d.closeRuntimeStateStores)

	store := d.sessionRuntimeStateStore(protocol.DefaultProjectID)
	if store == nil {
		t.Fatal("session runtime store nil")
	}
	if err := store.UpsertSessionState(context.Background(), d.canonicalProjectID(protocol.DefaultProjectID), daemonstate.Session{
		ID:        "sess-cmg",
		IssueID:   "cmg",
		State:     daemonstate.SessionStateRunning,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("upsert session state: %v", err)
	}

	if _, err := os.Stat(filepath.Join(worktree, ".azedarach", "azedarach.db")); err != nil {
		t.Fatalf("worktree runtime db stat: %v", err)
	}
	if got, want := reflect.ValueOf(store).Elem().FieldByName("dbPath").String(), filepath.Join(worktree, ".azedarach", "azedarach.db"); got != want {
		t.Fatalf("runtime store dbPath = %q, want %q", got, want)
	}
}
