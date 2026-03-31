package daemonclient

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/riordanpawley/azedarach/internal/daemon"
	"github.com/riordanpawley/azedarach/internal/ipc/transport"
)

func TestListTasksSnapshot_UsesSQLiteIssueStore(t *testing.T) {
	repoDir := t.TempDir()
	tmuxDir := t.TempDir()
	installFakeTmux(t, tmuxDir)
	t.Setenv("PATH", tmuxDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	seedIssueStore(t, repoDir, []seedTask{
		{
			id:          "afk",
			title:       "Wire sqlite issue store",
			description: "replace shell-based task access",
			status:      "open",
			priority:    1,
			issueType:   "task",
		},
		{
			id:          "afl",
			title:       "Validate daemon/tui boundaries",
			description: "ensure snapshots populate board",
			status:      "in_progress",
			priority:    2,
			issueType:   "feature",
		},
	})

	// Use a workspace-local temp dir so unix socket bind works in sandboxed test environments.
	runtimeDir, err := os.MkdirTemp(".", "azd-daemonclient-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	socketPath := filepath.Join(runtimeDir, "daemon.sock")
	lockPath := filepath.Join(runtimeDir, "daemon.lock")
	stop := startDaemonForTest(t, repoDir, socketPath, lockPath)
	defer stop()

	client := New(transport.NewClient(socketPath).WithTimeout(20 * time.Second)).WithProjectID("proj-sqlite")
	readCtx, cancelRead := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelRead()
	snapshot, err := client.ListTasksSnapshot(readCtx)
	if err != nil {
		t.Fatalf("ListTasksSnapshot error: %v", err)
	}
	if snapshot.Revision != 0 {
		t.Fatalf("snapshot revision = %d, want 0", snapshot.Revision)
	}
	if got, want := len(snapshot.Tasks), 2; got != want {
		t.Fatalf("snapshot task count = %d, want %d", got, want)
	}
	if snapshot.Tasks[0].ID == "" || snapshot.Tasks[1].ID == "" {
		t.Fatalf("snapshot task ids should be populated: %+v", snapshot.Tasks)
	}
}

func installFakeTmux(t *testing.T, dir string) {
	t.Helper()

	path := filepath.Join(dir, "tmux")
	script := []byte("#!/bin/sh\ncase \"$1\" in\n  list-sessions) exit 1 ;;\n  has-session|new-session|kill-session|send-keys|capture-pane|attach-session|set-environment|switch-client|display-popup) exit 0 ;;\n  *) exit 1 ;;\nesac\n")
	if err := os.WriteFile(path, script, 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
}

type seedTask struct {
	id          string
	title       string
	description string
	status      string
	priority    int
	issueType   string
}

func seedIssueStore(t *testing.T, repoDir string, tasks []seedTask) {
	t.Helper()

	dbDir := filepath.Join(repoDir, ".azedarach")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dbDir, err)
	}
	dbPath := filepath.Join(dbDir, "azedarach.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	schema := []string{
		`CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			description TEXT,
			status TEXT NOT NULL,
			priority INTEGER NOT NULL,
			issue_type TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			closed_at TEXT,
			assignee TEXT,
			labels_json TEXT,
			implementations_json TEXT,
			design TEXT,
			notes TEXT,
			acceptance TEXT,
			estimate INTEGER,
			deleted_at TEXT
		);`,
		`CREATE TABLE issue_dependencies (
			issue_id TEXT NOT NULL,
			depends_on_id TEXT NOT NULL,
			dependency_type TEXT NOT NULL,
			tombstoned_at TEXT,
			PRIMARY KEY (issue_id, depends_on_id, dependency_type)
		);`,
		`CREATE TABLE meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);`,
	}
	for _, stmt := range schema {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, task := range tasks {
		if _, err := db.Exec(
			`INSERT INTO issues (id, title, description, status, priority, issue_type, created_at, updated_at, deleted_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
			task.id, task.title, task.description, task.status, task.priority, task.issueType, now, now,
		); err != nil {
			t.Fatalf("insert issue %s: %v", task.id, err)
		}
	}
}

func startDaemonForTest(t *testing.T, repoDir, socketPath, lockPath string) func() {
	t.Helper()

	d := daemon.New(daemon.Config{
		RepoDir:    repoDir,
		SocketPath: socketPath,
		LockPath:   lockPath,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- d.Run(ctx)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case err := <-errCh:
			if isSocketPermissionError(err) {
				t.Skipf("sandbox does not permit unix socket bind: %v", err)
			}
			if err != nil {
				t.Fatalf("daemon failed to start: %v", err)
			}
			t.Fatalf("daemon exited before socket became ready")
		default:
		}
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			select {
			case err := <-errCh:
				t.Fatalf("daemon failed to start (socket timeout): %v", err)
			default:
				t.Fatalf("daemon socket not ready at %s", socketPath)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	return func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("daemon shutdown error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("daemon shutdown timed out")
		}
	}
}

func isSocketPermissionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "operation not permitted") || strings.Contains(text, "permission denied")
}
