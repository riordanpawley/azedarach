package app

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/daemon"
	"github.com/riordanpawley/azedarach/internal/ipc/transport"
)

func TestLoadIssuesCmd_UsesDaemonSQLiteSnapshot(t *testing.T) {
	repoDir := t.TempDir()
	seedModelIssueStore(t, repoDir, "agm", "Close go-bubbletea parity gaps", "open", 1, "epic")

	runtimeDir, err := os.MkdirTemp(".", "azd-model-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	socketPath := filepath.Join(runtimeDir, "daemon.sock")
	lockPath := filepath.Join(runtimeDir, "daemon.lock")
	stop := startModelTestDaemon(t, repoDir, socketPath, lockPath)
	defer stop()

	model := Model{
		daemonClient: daemonclient.New(transport.NewClient(socketPath)).WithProjectID("proj-model"),
	}
	msg := model.loadIssuesCmd()()

	loaded, ok := msg.(issuesLoadedMsg)
	if !ok {
		t.Fatalf("message type = %T, want issuesLoadedMsg", msg)
	}
	if got, want := len(loaded.tasks), 1; got != want {
		t.Fatalf("loaded tasks = %d, want %d", got, want)
	}
	if loaded.tasks[0].ID != "agm" {
		t.Fatalf("loaded task id = %q, want agm", loaded.tasks[0].ID)
	}
	if loaded.tasks[0].Title != "Close go-bubbletea parity gaps" {
		t.Fatalf("loaded task title = %q", loaded.tasks[0].Title)
	}
}

func seedModelIssueStore(t *testing.T, repoDir, id, title, status string, priority int, issueType string) {
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
	if _, err := db.Exec(
		`INSERT INTO issues (id, title, description, status, priority, issue_type, created_at, updated_at, deleted_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		id, title, "", status, priority, issueType, now, now,
	); err != nil {
		t.Fatalf("insert issue: %v", err)
	}
}

func startModelTestDaemon(t *testing.T, repoDir, socketPath, lockPath string) func() {
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
