package app

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

	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/cli"
	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/config"
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
	model.repoDir = repoDir
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
		statusByID[task.ID.String()] = string(task.Status)
		titleByID[task.ID.String()] = task.Title
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
	model.repoDir = repoDir
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
		openIDs[task.ID.String()] = struct{}{}
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

func TestIssueSnapshotParityAcrossCLIAndTUIBoard(t *testing.T) {
	repoDir := t.TempDir()
	parentID := "az-parent"
	childID := "az-child"
	dependentID := "az-dependent"
	seedModelIssueStore(t, repoDir, parentID, "Parent issue", "open", 2, "epic")
	seedModelIssueStore(t, repoDir, childID, "Child issue", "open", 1, "task")
	seedModelIssueStore(t, repoDir, dependentID, "Blocked issue", "open", 0, "bug")
	seedModelIssueDependency(t, repoDir, childID, parentID, "parent-child")
	seedModelIssueDependency(t, repoDir, dependentID, parentID, "blocks")

	socketPath, lockPath := newModelTestRuntimePaths(t)
	stop := startModelTestDaemon(t, repoDir, socketPath, lockPath)
	defer stop()

	client := daemonclient.New(transport.NewClient(socketPath)).WithProjectID("proj-model")
	cliDeps := &cli.Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: client,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj-model",
		RepoDir:      repoDir,
	}

	listOut := captureStdoutForParity(t, func() error {
		return cli.IssueListCommand(cliDeps, cli.IssueListOptions{Deps: true})
	})
	for _, want := range []string{
		"Top-level issues:",
		"Dependency links (listed issues):",
		"- az-child -> az-parent (parent-child)",
		"- az-dependent -> az-parent (blocks)",
	} {
		if !strings.Contains(listOut, want) {
			t.Fatalf("issue list output missing %q: %q", want, listOut)
		}
	}

	getOut := captureStdoutForParity(t, func() error {
		return cli.IssueGetCommand(cliDeps, cli.IssueGetOptions{IssueID: parentID})
	})
	for _, want := range []string{
		"ID: az-parent",
		"Title: Parent issue",
		"Status: open",
		"Priority: P2",
		"Type: epic",
		"Dependents:",
		"- az-child (parent-child, status=open)",
		"- az-dependent (blocks, status=open)",
	} {
		if !strings.Contains(getOut, want) {
			t.Fatalf("issue get output missing %q: %q", want, getOut)
		}
	}

	model := newTestModel()
	model.repoDir = repoDir
	model.daemonClient = client
	model.loading = false
	msg := model.loadIssuesCmd()()
	loaded, ok := msg.(issuesLoadedMsg)
	if !ok {
		t.Fatalf("loadIssuesCmd message type = %T, want issuesLoadedMsg", msg)
	}
	model.tasks = loaded.tasks
	rendered := ansi.Strip(model.View())

	for _, want := range []string{
		"Parent issue",
		"Blocked issue",
		"az-parent",
		"P2",
		"E",
		"B",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("tui board render missing %q: %q", want, rendered)
		}
	}
	if strings.Contains(rendered, "Child issue") {
		t.Fatalf("tui board should hide parent-child child task, got: %q", rendered)
	}
}

func TestIssueSnapshotParityAcrossCLIAndTUIJSONListFields(t *testing.T) {
	repoDir := t.TempDir()
	parentID := "az-parent-json"
	childID := "az-child-json"
	doneID := "az-done-json"
	blockedID := "az-blocked-json"

	seedModelIssueStore(t, repoDir, parentID, "Parent JSON", "open", 2, "epic")
	seedModelIssueStore(t, repoDir, childID, "Child JSON", "in_progress", 1, "task")
	seedModelIssueStore(t, repoDir, doneID, "Done JSON", "closed", 3, "feature")
	seedModelIssueStore(t, repoDir, blockedID, "Blocked JSON", "blocked", 0, "bug")
	seedModelIssueDependency(t, repoDir, childID, parentID, "parent-child")
	seedModelIssueDependency(t, repoDir, blockedID, parentID, "blocks")

	socketPath, lockPath := newModelTestRuntimePaths(t)
	stop := startModelTestDaemon(t, repoDir, socketPath, lockPath)
	defer stop()

	client := daemonclient.New(transport.NewClient(socketPath)).WithProjectID("proj-model")
	cliDeps := &cli.Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: client,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj-model",
		RepoDir:      repoDir,
	}

	jsonOut := captureStdoutForParity(t, func() error {
		return cli.IssueListCommand(cliDeps, cli.IssueListOptions{
			JSON:  true,
			IDs:   []string{parentID, childID, doneID, blockedID},
			Limit: 10,
		})
	})

	var cliTasks []domain.Task
	if err := json.Unmarshal([]byte(jsonOut), &cliTasks); err != nil {
		t.Fatalf("unmarshal CLI issue list JSON: %v\noutput=%q", err, jsonOut)
	}

	model := newTestModel()
	model.repoDir = repoDir
	model.daemonClient = client
	msg := model.loadIssuesCmd()()
	loaded, ok := msg.(issuesLoadedMsg)
	if !ok {
		t.Fatalf("loadIssuesCmd message type = %T, want issuesLoadedMsg", msg)
	}

	tuiByID := make(map[string]domain.Task, len(loaded.tasks))
	for _, task := range loaded.tasks {
		tuiByID[task.ID.String()] = task
	}

	for _, cliTask := range cliTasks {
		tuiTask, ok := tuiByID[cliTask.ID.String()]
		if !ok {
			t.Fatalf("task %q exists in CLI snapshot but missing from TUI snapshot", cliTask.ID)
		}
		if tuiTask.Title != cliTask.Title {
			t.Fatalf("title mismatch for %s: tui=%q cli=%q", cliTask.ID, tuiTask.Title, cliTask.Title)
		}
		if tuiTask.Status != cliTask.Status {
			t.Fatalf("status mismatch for %s: tui=%q cli=%q", cliTask.ID, tuiTask.Status, cliTask.Status)
		}
		if tuiTask.Priority != cliTask.Priority {
			t.Fatalf("priority mismatch for %s: tui=%q cli=%q", cliTask.ID, tuiTask.Priority, cliTask.Priority)
		}
		if tuiTask.Type != cliTask.Type {
			t.Fatalf("type mismatch for %s: tui=%q cli=%q", cliTask.ID, tuiTask.Type, cliTask.Type)
		}
		if len(tuiTask.Dependencies) != len(cliTask.Dependencies) {
			t.Fatalf("dependency count mismatch for %s: tui=%d cli=%d", cliTask.ID, len(tuiTask.Dependencies), len(cliTask.Dependencies))
		}
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

func captureStdoutForParity(t *testing.T, fn func() error) string {
	t.Helper()

	origStdout := os.Stdout
	defer func() {
		os.Stdout = origStdout
	}()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	err = fn()
	_ = w.Close()
	<-done
	_ = r.Close()
	if err != nil {
		t.Fatalf("captured command error: %v", err)
	}
	return buf.String()
}

func newModelTestRuntimePaths(t *testing.T) (string, string) {
	t.Helper()

	runtimeDir, err := os.MkdirTemp(".", "azd-")
	if err != nil {
		t.Fatalf("MkdirTemp(.): %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	return filepath.Join(runtimeDir, "s.sock"), filepath.Join(runtimeDir, "l.lock")
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
