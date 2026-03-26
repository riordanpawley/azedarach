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
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ipc/transport"
)

func TestLoadIssuesCmd_UsesDaemonSQLiteSnapshot(t *testing.T) {
	repoDir := t.TempDir()
	seedModelIssueStore(t, repoDir, "agm", "Close go-bubbletea parity gaps", "open", 1, "epic")
	seedModelIssueStore(t, repoDir, "agn", "Finish go-bubbletea parity lane", "closed", 2, "task")

	socketPath, lockPath := newModelTestRuntimePaths(t)
	stop := startModelTestDaemon(t, repoDir, socketPath, lockPath)
	defer stop()

	model := newTestModel()
	model.daemonClient = daemonclient.New(transport.NewClient(socketPath)).WithProjectID("proj-model")
	msg := model.loadIssuesCmd()()

	loaded, ok := msg.(issuesLoadedMsg)
	if !ok {
		t.Fatalf("message type = %T, want issuesLoadedMsg", msg)
	}
	if got, want := len(loaded.tasks), 2; got != want {
		t.Fatalf("loaded tasks = %d, want %d", got, want)
	}

	statusByID := make(map[string]string, len(loaded.tasks))
	titleByID := make(map[string]string, len(loaded.tasks))
	for _, task := range loaded.tasks {
		statusByID[task.ID] = string(task.Status)
		titleByID[task.ID] = task.Title
	}
	if got, want := statusByID["agm"], "open"; got != want {
		t.Fatalf("loaded task status for agm = %q, want %q", got, want)
	}
	if got, want := statusByID["agn"], "closed"; got != want {
		t.Fatalf("loaded task status for agn = %q, want %q", got, want)
	}
	if got, want := titleByID["agm"], "Close go-bubbletea parity gaps"; got != want {
		t.Fatalf("loaded task title for agm = %q, want %q", got, want)
	}
	if got, want := titleByID["agn"], "Finish go-bubbletea parity lane"; got != want {
		t.Fatalf("loaded task title for agn = %q, want %q", got, want)
	}

	model.tasks = loaded.tasks
	columns := model.buildColumns()
	if got, want := len(columns), 4; got != want {
		t.Fatalf("columns = %d, want %d", got, want)
	}
	if got, want := len(columns[0].Tasks), 1; got != want {
		t.Fatalf("open column tasks = %d, want %d", got, want)
	}
	if got, want := len(columns[3].Tasks), 1; got != want {
		t.Fatalf("closed column tasks = %d, want %d", got, want)
	}
	if columns[0].Tasks[0].ID != "agm" {
		t.Fatalf("open column task id = %q, want agm", columns[0].Tasks[0].ID)
	}
	if columns[3].Tasks[0].ID != "agn" {
		t.Fatalf("closed column task id = %q, want agn", columns[3].Tasks[0].ID)
	}
}

func TestLoadIssuesCmd_HidesParentChildTasksFromBoardByDefault(t *testing.T) {
	repoDir := t.TempDir()
	seedModelIssueStore(t, repoDir, "az-parent", "Parent issue", "open", 1, "epic")
	seedModelIssueStore(t, repoDir, "az-child", "Child issue", "open", 2, "task")
	seedModelIssueStore(t, repoDir, "az-blocks-only", "Blocks-only issue", "open", 2, "task")
	seedModelIssueDependency(t, repoDir, "az-child", "az-parent", "parent-child")
	seedModelIssueDependency(t, repoDir, "az-blocks-only", "az-parent", "blocks")

	socketPath, lockPath := newModelTestRuntimePaths(t)
	stop := startModelTestDaemon(t, repoDir, socketPath, lockPath)
	defer stop()

	model := newTestModel()
	model.daemonClient = daemonclient.New(transport.NewClient(socketPath)).WithProjectID("proj-model")
	msg := model.loadIssuesCmd()()
	loaded, ok := msg.(issuesLoadedMsg)
	if !ok {
		t.Fatalf("message type = %T, want issuesLoadedMsg", msg)
	}

	model.tasks = loaded.tasks
	columns := model.buildColumns()

	openIDs := make(map[string]struct{})
	for _, task := range columns[domain.StatusOpen.Column()].Tasks {
		openIDs[task.ID] = struct{}{}
	}
	if _, ok := openIDs["az-parent"]; !ok {
		t.Fatalf("expected parent issue to remain visible in open column: %+v", columns[domain.StatusOpen.Column()].Tasks)
	}
	if _, ok := openIDs["az-blocks-only"]; !ok {
		t.Fatalf("expected blocks-only issue to remain visible in open column: %+v", columns[domain.StatusOpen.Column()].Tasks)
	}
	if _, ok := openIDs["az-child"]; ok {
		t.Fatalf("expected parent-child issue to be hidden from board open column: %+v", columns[domain.StatusOpen.Column()].Tasks)
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
		`CREATE TABLE IF NOT EXISTS issues (
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
		`CREATE TABLE IF NOT EXISTS issue_dependencies (
			issue_id TEXT NOT NULL,
			depends_on_id TEXT NOT NULL,
			dependency_type TEXT NOT NULL,
			tombstoned_at TEXT,
			PRIMARY KEY (issue_id, depends_on_id, dependency_type)
		);`,
		`CREATE TABLE IF NOT EXISTS meta (
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

func seedModelIssueDependency(t *testing.T, repoDir, issueID, dependsOnID, dependencyType string) {
	t.Helper()
	dbPath := filepath.Join(repoDir, ".azedarach", "azedarach.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(
		`INSERT INTO issue_dependencies (issue_id, depends_on_id, dependency_type, tombstoned_at) VALUES (?, ?, ?, NULL)`,
		issueID, dependsOnID, dependencyType,
	); err != nil {
		t.Fatalf("insert dependency: %v", err)
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

func newModelTestRuntimePaths(t *testing.T) (string, string) {
	t.Helper()

	runtimeDir, err := os.MkdirTemp("/tmp", "azd-")
	if err != nil {
		t.Fatalf("MkdirTemp(/tmp): %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	return filepath.Join(runtimeDir, "s.sock"), filepath.Join(runtimeDir, "l.lock")
}
