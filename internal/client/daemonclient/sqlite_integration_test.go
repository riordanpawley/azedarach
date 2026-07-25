package daemonclient

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
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

	"github.com/riordanpawley/azedarach/internal/config"
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

	projectID, err := config.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("derive fixture project ID: %v", err)
	}
	client := New(transport.NewClient(socketPath).WithTimeout(20 * time.Second)).
		WithProjectID(projectID).
		WithReadWaitPolicy(ReadWaitPolicy{
			Default:  15 * time.Second,
			Explicit: 20 * time.Second,
		})
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

func TestDaemonClientTestDaemonDoesNotOpenProjectsRegisteredInCallerHome(t *testing.T) {
	callerHome := t.TempDir()
	liveRepoDir := t.TempDir()
	seedIssueStore(t, liveRepoDir, []seedTask{{
		id: "live-sentinel", title: "Must remain untouched", status: "open", priority: 1, issueType: "bug",
	}})

	liveDBPath := filepath.Join(liveRepoDir, ".azedarach", "azedarach.db")
	liveDB, err := sql.Open("sqlite", "file:"+liveDBPath+"?_pragma=busy_timeout(1)")
	if err != nil {
		t.Fatalf("open live sentinel database: %v", err)
	}
	defer liveDB.Close()
	if _, err := liveDB.Exec("BEGIN EXCLUSIVE"); err != nil {
		t.Fatalf("lock live sentinel database: %v", err)
	}
	defer func() { _, _ = liveDB.Exec("ROLLBACK") }()

	registryData, err := json.Marshal(config.ProjectsRegistry{
		Projects: []config.Project{{ID: "live-sentinel", Name: "Live sentinel", Path: liveRepoDir}},
	})
	if err != nil {
		t.Fatalf("marshal caller-home projects registry: %v", err)
	}
	registryDir := filepath.Join(callerHome, ".config", "azedarach")
	if err := os.MkdirAll(registryDir, 0o755); err != nil {
		t.Fatalf("create caller-home registry directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(registryDir, "projects.json"), registryData, 0o644); err != nil {
		t.Fatalf("write caller-home projects registry: %v", err)
	}
	before, err := os.ReadFile(liveDBPath)
	if err != nil {
		t.Fatalf("read live sentinel database before fixture: %v", err)
	}

	t.Setenv("HOME", callerHome)
	repoDir := t.TempDir()
	seedIssueStore(t, repoDir, []seedTask{{
		id: "fixture", title: "Fixture issue", status: "open", priority: 1, issueType: "task",
	}})
	runtimeDir, err := os.MkdirTemp(".", "azd-client-isolation-")
	if err != nil {
		t.Fatalf("create short runtime directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	stop := startDaemonForTest(t, repoDir, filepath.Join(runtimeDir, "daemon.sock"), filepath.Join(runtimeDir, "daemon.lock"))
	stop()

	after, err := os.ReadFile(liveDBPath)
	if err != nil {
		t.Fatalf("read live sentinel database after fixture: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("registered live sentinel database was modified by test daemon fixture")
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

	// Daemon startup discovers every project registered under the user's home.
	// Keep integration fixtures in a private namespace so tests cannot open or
	// migrate registered live project databases.
	t.Setenv("HOME", t.TempDir())

	d := daemon.New(daemon.Config{
		RepoDir:     repoDir,
		SocketPath:  socketPath,
		LockPath:    lockPath,
		IdleTimeout: 250 * time.Millisecond,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- d.Run(ctx)
	}()

	select {
	case <-d.Ready():
	case err := <-errCh:
		if isSocketPermissionError(err) {
			t.Skipf("sandbox does not permit unix socket bind: %v", err)
		}
		if err != nil {
			t.Fatalf("daemon failed to start: %v", err)
		}
		t.Fatalf("daemon exited before socket became ready")
	}

	return func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Fatalf("daemon shutdown error: %v", err)
			}
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
