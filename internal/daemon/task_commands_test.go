package daemon

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	daemonops "github.com/riordanpawley/azedarach/internal/daemon/operations"
	opstore "github.com/riordanpawley/azedarach/internal/daemon/operations/store"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

func TestBuildTaskSnapshotExportBodyIsDeterministic(t *testing.T) {
	originalNow := timeNow
	t.Cleanup(func() {
		timeNow = originalNow
	})
	timeNow = func() time.Time {
		return time.Date(2026, 3, 25, 12, 34, 56, 789000000, time.UTC)
	}
	wantCapturedAt := time.Date(2026, 3, 25, 12, 34, 56, 789000000, time.UTC).UnixMilli()

	parentID := naming.IssueID("parent-1")
	body := buildTaskSnapshotExportBody(
		"proj",
		17,
		[]domain.Task{
			{
				ID:           "b-task",
				Title:        "Bravo",
				Status:       domain.StatusOpen,
				Priority:     domain.P2,
				Type:         domain.TypeTask,
				UpdatedAt:    time.Now(),
				Dependencies: []domain.Dependency{{ID: "dep-1", Type: domain.DependencyBlocks}},
			},
			{
				ID:           "a-task",
				Title:        "Alpha",
				Status:       domain.StatusInProgress,
				Priority:     domain.P1,
				Type:         domain.TypeBug,
				ParentID:     &parentID,
				Dependencies: []domain.Dependency{{ID: "dep-2", Type: domain.DependencyRelatedTo}, {ID: "dep-3", Type: domain.DependencyBlocks}},
			},
		},
		[]string{"b-task", "a-task"},
		"/tmp/proj",
	)

	if got, want := body.SchemaVersion, uint16(1); got != want {
		t.Fatalf("SchemaVersion = %d, want %d", got, want)
	}
	if got, want := body.SnapshotRevision, uint64(17); got != want {
		t.Fatalf("SnapshotRevision = %d, want %d", got, want)
	}
	if got, want := body.CapturedAtMs, wantCapturedAt; got != want {
		t.Fatalf("CapturedAtMs = %d, want %d", got, want)
	}
	if got, want := body.ProjectID, "proj"; got != want {
		t.Fatalf("ProjectID = %q, want %q", got, want)
	}
	if got, want := body.TaskCount, 2; got != want {
		t.Fatalf("TaskCount = %d, want %d", got, want)
	}
	if got, want := body.SessionCount, 2; got != want {
		t.Fatalf("SessionCount = %d, want %d", got, want)
	}

	if got, want := len(body.Tasks), 2; got != want {
		t.Fatalf("len(Tasks) = %d, want %d", got, want)
	}
	if got, want := body.Tasks[0].ID, "a-task"; got != want {
		t.Fatalf("Tasks[0].ID = %q, want %q", got, want)
	}
	if got, want := body.Tasks[0].SessionAttached, true; got != want {
		t.Fatalf("Tasks[0].SessionAttached = %v, want %v", got, want)
	}
	if got, want := body.Tasks[0].DependencyCount, 2; got != want {
		t.Fatalf("Tasks[0].DependencyCount = %d, want %d", got, want)
	}
	if got, want := body.Tasks[0].Critical, false; got != want {
		t.Fatalf("Tasks[0].Critical = %v, want %v", got, want)
	}
	if body.Tasks[0].ParentID == nil || *body.Tasks[0].ParentID != parentID.String() {
		t.Fatalf("Tasks[0].ParentID = %+v, want %q", body.Tasks[0].ParentID, parentID.String())
	}
	if got, want := body.Tasks[1].ID, "b-task"; got != want {
		t.Fatalf("Tasks[1].ID = %q, want %q", got, want)
	}
	if got, want := body.Tasks[1].Critical, false; got != want {
		t.Fatalf("Tasks[1].Critical = %v, want %v", got, want)
	}

	if got, want := len(body.Sessions), 2; got != want {
		t.Fatalf("len(Sessions) = %d, want %d", got, want)
	}
	if got, want := body.Sessions[0].Name, "a-task"; got != want {
		t.Fatalf("Sessions[0].Name = %q, want %q", got, want)
	}
	if got, want := body.Sessions[1].Name, "b-task"; got != want {
		t.Fatalf("Sessions[1].Name = %q, want %q", got, want)
	}

	if _, err := json.Marshal(body); err != nil {
		t.Fatalf("marshal snapshot body: %v", err)
	}
}

func TestProjectIssueStoreMigrationFailureSuppressesRepeatedPolls(t *testing.T) {
	originalNow := timeNow
	now := time.Date(2026, 7, 7, 6, 0, 0, 0, time.UTC)
	timeNow = func() time.Time {
		return now
	}
	t.Cleanup(func() {
		timeNow = originalNow
	})

	ctx := context.Background()
	repoDir := t.TempDir()
	dbPath := filepath.Join(repoDir, ".azedarach", "azedarach.db")
	seedIssueStoreInvalidGraphClosureObject(t, dbPath)
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	issuesClient := issues.NewClientAtPath(dbPath, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	tmuxRunner := &testTmuxRunner{
		sessions:    map[string]bool{},
		panes:       map[string][]string{},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}
	d := &Daemon{
		cfg: Config{
			RepoDir: repoDir,
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		tmux:   tmux.NewClient(tmuxRunner, slog.New(slog.NewTextHandler(io.Discard, nil))),
		issues: issuesClient,
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		issueClientsByRoot: map[string]*issues.Client{
			daemonStoreRootKey(repoDir): issuesClient,
		},
		projectIssueStoreHealthByProject: map[string]projectIssueStoreHealthState{},
		revision:                         map[string]uint64{projectID: 1},
	}

	first, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-broken-task-list",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.list",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
	})
	if err != nil {
		t.Fatalf("handleTaskList returned error: %v", err)
	}
	if first.OK || first.Error == nil {
		t.Fatalf("first task.list response OK = %v, error = %+v; want migration error", first.OK, first.Error)
	}
	if got := first.Error.Message; !strings.Contains(got, "apply migration 0018_issue_graph_closure") || !strings.Contains(got, "issue_graph_closure") {
		t.Fatalf("first task.list error = %q, want issue_graph_closure migration context", got)
	}

	removeSQLiteFiles(t, dbPath)
	board, err := d.handleBoardFetch(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-cached-board-fetch",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         protocol.CommandBoardFetch,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
	})
	if err != nil {
		t.Fatalf("handleBoardFetch returned error: %v", err)
	}
	if board.OK || board.Error == nil {
		t.Fatalf("cached board.fetch response OK = %v, error = %+v; want cached health error", board.OK, board.Error)
	}
	if got := board.Error.Message; !strings.Contains(got, "project issue store unhealthy (cached)") {
		t.Fatalf("cached board.fetch error = %q, want cached health message", got)
	}

	second, err := d.handleSessionStatus(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-cached-session-status",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.status",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            marshalJSON(map[string]string{"project_id": projectID}),
	})
	if err != nil {
		t.Fatalf("handleSessionStatus returned error: %v", err)
	}
	if second.OK || second.Error == nil {
		t.Fatalf("cached session.status response OK = %v, error = %+v; want cached health error", second.OK, second.Error)
	}
	if got := second.Error.Message; !strings.Contains(got, "project issue store unhealthy (cached)") || !strings.Contains(got, "suppressing repeated polling") {
		t.Fatalf("cached session.status error = %q, want cached health message", got)
	}
	if got := tmuxRunner.listSessionCallCount(); got != 0 {
		t.Fatalf("tmux list-sessions calls after cached health = %d, want 0", got)
	}

	now = now.Add(projectIssueStoreHealthBackoff + time.Second)
	third, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-retry-after-backoff",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.list",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
	})
	if err != nil {
		t.Fatalf("retry task.list returned error: %v", err)
	}
	if !third.OK {
		t.Fatalf("retry task.list response error = %+v, want success after backoff expiry", third.Error)
	}
}

func seedIssueStoreInvalidGraphClosureObject(t *testing.T, dbPath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatalf("mkdir db dir: %v", err)
	}
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s", filepath.ToSlash(dbPath)))
	if err != nil {
		t.Fatalf("open sqlite fixture: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE schema_migrations (
			id TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL
		);
		CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			description TEXT,
			notes TEXT,
			design TEXT,
			acceptance TEXT,
			assignee TEXT,
			labels_json TEXT,
			status TEXT NOT NULL,
			priority INTEGER NOT NULL,
			issue_type TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT
		);
		CREATE VIEW issue_graph_closure AS
			SELECT
				'default' AS project_id,
				'' AS ancestor_id,
				'' AS descendant_id,
				'parent-child' AS dependency_type,
				0 AS depth,
				'1970-01-01T00:00:00Z' AS updated_at
			WHERE 0;
	`); err != nil {
		t.Fatalf("create legacy issue store fixture: %v", err)
	}
	applied := []string{
		"0001_bootstrap_tables",
		"0002_dependency_foreign_keys",
		"0003_issue_indexes",
		"0004_spec_tables",
		"0005_spec_audit_log",
		"0006_external_issue_sync",
		"0006_issue_external_refs",
		"0007_external_issue_sync_payload",
		"0008_decision_tables",
		"0009_decision_audit_log",
		"0010_decisions_refresh",
		"0011_decisions_consequences",
		"0012_blocked_status_to_open",
		"0013_closed_runtime_invariants",
		"0014_linear_sync_external_refs_backfill",
		"0015_issue_attachments",
		"0016_issue_search_fts",
		"0017_spec_requirement_search_fts",
	}
	for _, id := range applied {
		if _, err := db.Exec(`INSERT INTO schema_migrations (id, applied_at) VALUES (?, ?)`, id, "2026-07-07T00:00:00Z"); err != nil {
			t.Fatalf("seed migration %s: %v", id, err)
		}
	}
	if _, err := db.Exec(`
		INSERT INTO issues (
			id, title, description, notes, design, acceptance, assignee, labels_json,
			status, priority, issue_type, created_at, updated_at
		)
		VALUES ('cvn-fixture', 'Broken migration fixture', '', '', '', '', '', '[]', 'open', 1, 'bug', '2026-07-07T00:00:00Z', '2026-07-07T00:00:00Z')
	`); err != nil {
		t.Fatalf("seed legacy issue row: %v", err)
	}
}

func removeSQLiteFiles(t *testing.T, dbPath string) {
	t.Helper()
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("remove %s: %v", path, err)
		}
	}
}

func TestValidateDependencyEndpointProjectsRejectsCrossProjectRefs(t *testing.T) {
	tests := []struct {
		name             string
		requestProject   string
		issueProject     string
		dependsOnProject string
		wantErr          string
	}{
		{
			name:             "same declared project passes",
			requestProject:   "chefy",
			issueProject:     "chefy",
			dependsOnProject: "chefy",
		},
		{
			name:             "mixed endpoint projects rejected",
			requestProject:   "chefy",
			issueProject:     "chefy",
			dependsOnProject: "azedarach",
			wantErr:          "dependency endpoints must be in the same project",
		},
		{
			name:             "issue endpoint project must match request project",
			requestProject:   "azedarach",
			issueProject:     "chefy",
			dependsOnProject: "chefy",
			wantErr:          `issue_project_id "chefy" does not match request project "azedarach"`,
		},
		{
			name:             "depends endpoint project must match request project",
			requestProject:   "azedarach",
			dependsOnProject: "chefy",
			wantErr:          `depends_on_project_id "chefy" does not match request project "azedarach"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDependencyEndpointProjects(tt.requestProject, tt.issueProject, tt.dependsOnProject)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateDependencyEndpointProjects() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateDependencyEndpointProjects() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestHandleTaskDependencyAddRejectsCrossProjectEndpointMetadata(t *testing.T) {
	logger := slog.Default()
	issuesClient := issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), logger)
	t.Cleanup(func() {
		if err := issuesClient.CloseDB(); err != nil {
			t.Fatalf("close issues db: %v", err)
		}
	})

	d := &Daemon{
		cfg: Config{Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			"chefy": issuesClient,
		},
		revision: map[string]uint64{"chefy": 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	body, err := json.Marshal(map[string]any{
		"task_id":               "az-1",
		"depends_on_id":         "az-2",
		"dependency_type":       "blocks",
		"issue_project_id":      "chefy",
		"depends_on_project_id": "azedarach",
	})
	if err != nil {
		t.Fatalf("marshal dependency add body: %v", err)
	}

	resp, err := d.handleTaskDependencyAdd(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-cross-project-dep",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID("chefy")},
		Command:         "task.dependency.add",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskDependencyAdd error: %v", err)
	}
	if resp.OK {
		t.Fatalf("handleTaskDependencyAdd OK = true, want rejection")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInvalidRequest || !strings.Contains(resp.Error.Message, "dependency endpoints must be in the same project") {
		t.Fatalf("response error = %+v, want invalid cross-project dependency", resp.Error)
	}
}

func TestBuildTaskSnapshotExportBody_ProjectScopedSessionPrefixMatchesIssue(t *testing.T) {
	body := buildTaskSnapshotExportBody(
		"Chefy",
		1,
		[]domain.Task{
			{
				ID:       "em",
				Title:    "Issue with tmux session",
				Status:   domain.StatusInProgress,
				Priority: domain.P2,
				Type:     domain.TypeTask,
			},
		},
		[]string{"ch-em"},
		"Chefy",
	)

	if got, want := len(body.Tasks), 1; got != want {
		t.Fatalf("len(Tasks) = %d, want %d", got, want)
	}
	if !body.Tasks[0].SessionAttached {
		t.Fatalf("SessionAttached = false, want true")
	}
}

func TestBuildBoardSnapshotPayloadOmitsDetailFields(t *testing.T) {
	payload := buildBoardSnapshotPayload(
		"proj-board",
		12,
		time.Date(2026, time.April, 2, 11, 2, 0, 0, time.UTC),
		protocol.TaskListFreshnessFresh,
		[]domain.Task{{
			ID:          "az-board",
			Title:       "Board task",
			Description: "large description",
			Notes:       "large notes",
			Design:      "large design",
			Acceptance:  "large acceptance",
			Status:      domain.StatusInProgress,
			Priority:    domain.P1,
			Type:        domain.TypeTask,
		}},
	)
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal board payload: %v", err)
	}
	for _, field := range []string{"description", "notes", "design", "acceptance"} {
		if bytes.Contains(body, []byte(field)) {
			t.Fatalf("board payload contains %q field: %s", field, string(body))
		}
	}

	decoded, err := protocol.DecodeBoardSnapshotPayload(body)
	if err != nil {
		t.Fatalf("decode board payload: %v", err)
	}
	if got, want := decoded.SchemaVersion, uint16(protocol.BoardSnapshotSchemaVersion); got != want {
		t.Fatalf("schema version = %d, want %d", got, want)
	}
	if got, want := len(decoded.Tasks), 1; got != want {
		t.Fatalf("task count = %d, want %d", got, want)
	}
	if got, want := decoded.Tasks[0].Title, "Board task"; got != want {
		t.Fatalf("title = %q, want %q", got, want)
	}
}

func TestHandleTaskListIsReadOnlyAndUsesProjectionData(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	originalNow := timeNow
	t.Cleanup(func() {
		timeNow = originalNow
	})
	timeNow = func() time.Time {
		return time.Date(2026, time.April, 2, 11, 2, 5, 0, time.UTC)
	}

	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() {
		if err := issuesClient.CloseDB(); err != nil {
			t.Fatalf("close issues db: %v", err)
		}
	})

	projectID := "proj-read-only"
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:       "Read-only task list",
		Description: "Long description should stay out of task.list summaries",
		Design:      "Long design should stay out of task.list summaries",
		Notes:       "Long notes should stay out of task.list summaries",
		Acceptance:  "Long acceptance should stay out of task.list summaries",
		Type:        domain.TypeTask,
		Priority:    domain.P1,
		Status:      domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	sessionID := naming.CanonicalSessionID(projectID, taskID)
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	sessionStartedAt := time.Date(2026, time.April, 2, 10, 59, 0, 0, time.UTC)
	sessionUpdatedAt := time.Date(2026, time.April, 2, 11, 0, 0, 0, time.UTC)
	if err := runtimeStateStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   taskID,
		State:     daemonstate.SessionStateAttached,
		StartedAt: &sessionStartedAt,
		UpdatedAt: sessionUpdatedAt,
	}); err != nil {
		t.Fatalf("seed projected session: %v", err)
	}

	rawStatus, err := json.Marshal(git.GitStatus{
		Added:          []string{"new.go"},
		Modified:       []string{"changed.go"},
		Staged:         []string{"staged.go"},
		Deleted:        []string{"removed.go"},
		HasChanges:     true,
		GitAdditions:   9,
		GitDeletions:   2,
		GitAheadCount:  4,
		GitBehindCount: 1,
	})
	if err != nil {
		t.Fatalf("marshal git status: %v", err)
	}
	worktreePath := "/tmp/proj-read-only-" + taskID
	if err := runtimeStateStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      worktreePath,
		Branch:    "riordan/" + taskID + "/read-only",
		UpdatedAt: time.Date(2026, time.April, 2, 11, 1, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed projected worktree: %v", err)
	}
	if err := runtimeStateStore.UpsertWorktreeStateGitStatus(ctx, projectID, taskID, rawStatus, time.Date(2026, time.April, 2, 11, 2, 0, 0, time.UTC)); err != nil {
		t.Fatalf("seed projected worktree status: %v", err)
	}

	d := &Daemon{
		cfg: Config{
			RepoDir: ".",
			Logger:  logger,
		},
		issues: issuesClient,
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		sessionStore: daemonstate.NewStore(),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
		revision: map[string]uint64{projectID: 7},
		tmux:     nil,
		git:      &git.Client{},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			".": &git.WorktreeManager{},
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: &git.WorktreeManager{},
		},
	}

	resp, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-list",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.list",
	})
	if err != nil {
		t.Fatalf("handleTaskList error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.list response = %+v", resp.Error)
	}
	if got, want := resp.Revision, uint64(7); got != want {
		t.Fatalf("response revision = %d, want %d", got, want)
	}
	if got, want := d.currentRevision(projectID), uint64(7); got != want {
		t.Fatalf("daemon revision = %d, want %d", got, want)
	}

	var payload protocol.TaskListSnapshotPayload
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("unmarshal task list body: %v", err)
	}
	if got, want := payload.SchemaVersion, protocol.TaskListSnapshotSchemaVersion; got != want {
		t.Fatalf("payload.SchemaVersion = %d, want %d", got, want)
	}
	if got, want := payload.ProjectID.String(), projectID; got != want {
		t.Fatalf("payload.ProjectID = %q, want %q", got, want)
	}
	if got, want := payload.SnapshotRevision, uint64(7); got != want {
		t.Fatalf("payload.SnapshotRevision = %d, want %d", got, want)
	}
	wantLastCheckedAt := time.Date(2026, time.April, 2, 11, 2, 0, 0, time.UTC)
	if got, want := payload.LastCheckedAt, wantLastCheckedAt; !got.Equal(want) {
		t.Fatalf("payload.LastCheckedAt = %v, want %v", got, want)
	}
	if got, want := payload.Freshness, protocol.TaskListFreshnessFresh; got != want {
		t.Fatalf("payload.Freshness = %q, want %q", got, want)
	}
	if !payload.SummariesOnly {
		t.Fatal("payload.SummariesOnly = false, want true")
	}
	tasks := payload.Tasks
	if got, want := len(tasks), 1; got != want {
		t.Fatalf("task count = %d, want %d", got, want)
	}

	task := tasks[0]
	if task.ID.String() != taskID {
		t.Fatalf("task.ID = %q, want %q", task.ID, taskID)
	}
	if task.Title != "Read-only task list" {
		t.Fatalf("task.Title = %q, want %q", task.Title, "Read-only task list")
	}
	if task.Description != "" || task.Design != "" || task.Notes != "" || task.Acceptance != "" {
		t.Fatalf("task.list returned full detail fields: description=%q design=%q notes=%q acceptance=%q", task.Description, task.Design, task.Notes, task.Acceptance)
	}
	if task.Session == nil {
		t.Fatal("expected task session projection")
	}
	if task.Session.State != domain.SessionBusy {
		t.Fatalf("task.Session.State = %q, want %q", task.Session.State, domain.SessionBusy)
	}
	if task.Session.StartedAt == nil || !task.Session.StartedAt.Equal(sessionStartedAt.UTC()) {
		t.Fatalf("task.Session.StartedAt = %v, want %v", task.Session.StartedAt, sessionStartedAt.UTC())
	}
	if task.Session.Worktree != worktreePath {
		t.Fatalf("task.Session.Worktree = %q, want %q", task.Session.Worktree, worktreePath)
	}
	if !task.HasWorktree {
		t.Fatal("task.HasWorktree = false, want true")
	}
	if !task.HasUncommittedChanges {
		t.Fatal("task.HasUncommittedChanges = false, want true")
	}
	if got, want := task.GitAdditions, 9; got != want {
		t.Fatalf("task.GitAdditions = %d, want %d", got, want)
	}
	if got, want := task.GitDeletions, 2; got != want {
		t.Fatalf("task.GitDeletions = %d, want %d", got, want)
	}
	if got, want := task.GitAheadCount, 4; got != want {
		t.Fatalf("task.GitAheadCount = %d, want %d", got, want)
	}
	if got, want := task.GitBehindCount, 1; got != want {
		t.Fatalf("task.GitBehindCount = %d, want %d", got, want)
	}

	sessions, err := runtimeStateStore.ListSessionStates(ctx, projectID)
	if err != nil {
		t.Fatalf("list projected sessions: %v", err)
	}
	if got, want := len(sessions), 1; got != want {
		t.Fatalf("projected session count = %d, want %d", got, want)
	}
	if sessions[0].State != daemonstate.SessionStateAttached {
		t.Fatalf("projected session state = %q, want %q", sessions[0].State, daemonstate.SessionStateAttached)
	}
	if !sessions[0].UpdatedAt.Equal(sessionUpdatedAt) {
		t.Fatalf("projected session updated_at = %v, want %v", sessions[0].UpdatedAt, sessionUpdatedAt)
	}

	worktrees, err := runtimeStateStore.ListWorktreeStates(ctx, projectID)
	if err != nil {
		t.Fatalf("list projected worktrees: %v", err)
	}
	if got, want := len(worktrees), 1; got != want {
		t.Fatalf("projected worktree count = %d, want %d", got, want)
	}
	if worktrees[0].IssueID != taskID {
		t.Fatalf("projected worktree issue_id = %q, want %q", worktrees[0].IssueID, taskID)
	}
	if !bytes.Equal(worktrees[0].GitStatusRaw, rawStatus) {
		t.Fatalf("projected worktree git status was mutated")
	}

	body, err := json.Marshal(map[string]string{"task_id": taskID})
	if err != nil {
		t.Fatalf("marshal task get request: %v", err)
	}
	getResp, err := d.handleTaskGet(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-get-after-summary-list",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.get",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskGet error: %v", err)
	}
	if !getResp.OK {
		t.Fatalf("task.get response = %+v", getResp.Error)
	}
	getPayload, err := protocol.DecodeTaskListSnapshotPayload(getResp.Body)
	if err != nil {
		t.Fatalf("decode task.get body: %v", err)
	}
	if len(getPayload.Tasks) == 0 {
		t.Fatal("task.get returned no tasks")
	}
	if getPayload.SummariesOnly {
		t.Fatal("task.get summaries_only = true, want false")
	}
	if got, want := getPayload.Tasks[0].Description, "Long description should stay out of task.list summaries"; got != want {
		t.Fatalf("task.get description = %q, want %q", got, want)
	}
	if got, want := getPayload.Tasks[0].Design, "Long design should stay out of task.list summaries"; got != want {
		t.Fatalf("task.get design = %q, want %q", got, want)
	}
}

func TestHandleTaskListRefreshesMissingTmuxSessionBeforeReportingActive(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	repoDir := t.TempDir()
	projectID := "proj-task-stale-session"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := issues.NewClient(repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Task list stale session",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	sessionID := naming.CanonicalSessionID(projectID, taskID)
	if err := runtimeStateStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID:            sessionID,
		IssueID:       taskID,
		State:         daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateAttached,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed projected session: %v", err)
	}

	d := &Daemon{
		cfg:          Config{RepoDir: repoDir, Logger: logger},
		issues:       issuesClient,
		sessionStore: daemonstate.NewStore(),
		tmux:         tmux.NewClient(&testTmuxRunner{sessions: map[string]bool{}}, logger),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 1},
	}
	d.runtimeProjectionWriter = newRuntimeProjectionWriter(d)

	resp, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-list-stale-session",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.list",
	})
	if err != nil {
		t.Fatalf("handleTaskList error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.list response = %+v", resp.Error)
	}

	var payload protocol.TaskListSnapshotPayload
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("unmarshal task list body: %v", err)
	}
	if len(payload.Tasks) != 1 {
		t.Fatalf("task count = %d, want 1", len(payload.Tasks))
	}
	if payload.Tasks[0].HasTmuxSession || payload.Tasks[0].Session != nil {
		t.Fatalf("task runtime session = %+v has_tmux=%v, want inactive after live tmux refresh", payload.Tasks[0].Session, payload.Tasks[0].HasTmuxSession)
	}

	row, found, err := runtimeStateStore.GetSessionState(ctx, projectID, sessionID)
	if err != nil {
		t.Fatalf("get projected session: %v", err)
	}
	if !found {
		t.Fatal("expected projected session row")
	}
	if row.ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("observed state = %s, want stopped", row.ObservedState)
	}
}

func TestHandleTaskListThrottlesSessionRuntimeRefresh(t *testing.T) {
	originalNow := timeNow
	now := time.Date(2026, time.July, 7, 3, 30, 0, 0, time.UTC)
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = originalNow })

	ctx := context.Background()
	logger := slog.Default()
	repoDir := t.TempDir()
	projectID := "proj-task-list-refresh-throttle"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := issues.NewClient(repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Task list refresh throttle",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	sessionID := naming.CanonicalSessionID(projectID, taskID)
	if err := runtimeStateStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID:            sessionID,
		IssueID:       taskID,
		State:         daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateAttached,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("seed projected session: %v", err)
	}

	tmuxRunner := &testTmuxRunner{sessions: map[string]bool{}}
	d := &Daemon{
		cfg:          Config{RepoDir: repoDir, Logger: logger},
		issues:       issuesClient,
		sessionStore: daemonstate.NewStore(),
		tmux:         tmux.NewClient(tmuxRunner, logger),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 1},
	}
	d.runtimeProjectionWriter = newRuntimeProjectionWriter(d)

	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-list-refresh-throttle",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.list",
	}
	if resp, err := d.handleTaskList(ctx, req); err != nil {
		t.Fatalf("first handleTaskList error: %v", err)
	} else if !resp.OK {
		t.Fatalf("first task.list response = %+v", resp.Error)
	}
	if got := tmuxRunner.listSessionCallCount(); got != 1 {
		t.Fatalf("tmux list-sessions calls after first task.list = %d, want 1", got)
	}

	now = now.Add(time.Second)
	req.RequestID = "req-task-list-refresh-throttle-second"
	if resp, err := d.handleTaskList(ctx, req); err != nil {
		t.Fatalf("second handleTaskList error: %v", err)
	} else if !resp.OK {
		t.Fatalf("second task.list response = %+v", resp.Error)
	}
	if got := tmuxRunner.listSessionCallCount(); got != 1 {
		t.Fatalf("tmux list-sessions calls after throttled task.list = %d, want 1", got)
	}

	now = now.Add(taskListRuntimeRefreshTTL + time.Millisecond)
	req.RequestID = "req-task-list-refresh-throttle-third"
	if resp, err := d.handleTaskList(ctx, req); err != nil {
		t.Fatalf("third handleTaskList error: %v", err)
	} else if !resp.OK {
		t.Fatalf("third task.list response = %+v", resp.Error)
	}
	if got := tmuxRunner.listSessionCallCount(); got != 2 {
		t.Fatalf("tmux list-sessions calls after throttle expiry = %d, want 2", got)
	}
}

func TestTaskListSessionRuntimeRefreshSharesInFlightRefresh(t *testing.T) {
	originalNow := timeNow
	now := time.Date(2026, time.July, 7, 3, 45, 0, 0, time.UTC)
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = originalNow })

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	const (
		projectID = "proj-task-list-refresh-shared"
		issueID   = "az-1"
	)
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	if err := runtimeStateStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID:            naming.CanonicalSessionID(projectID, issueID),
		IssueID:       issueID,
		State:         daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateAttached,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("seed projected session: %v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	tmuxRunner := &testTmuxRunner{
		sessions:            map[string]bool{},
		listSessionsEntered: entered,
		listSessionsRelease: release,
		killEntered:         make(chan struct{}),
		killRelease:         make(chan struct{}),
	}
	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		sessionStore: daemonstate.NewStore(),
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
	}
	d.runtimeProjectionWriter = newRuntimeProjectionWriter(d)

	type result struct {
		runtimeAt time.Time
		refreshed bool
		err       error
	}
	firstCh := make(chan result, 1)
	go func() {
		runtimeAt, refreshed, err := d.refreshTaskListSessionRuntimeState(ctx, projectID)
		firstCh <- result{runtimeAt: runtimeAt, refreshed: refreshed, err: err}
	}()

	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for first refresh to enter tmux: %v", ctx.Err())
	}

	secondCh := make(chan result, 1)
	go func() {
		runtimeAt, refreshed, err := d.refreshTaskListSessionRuntimeState(ctx, projectID)
		secondCh <- result{runtimeAt: runtimeAt, refreshed: refreshed, err: err}
	}()
	close(release)

	first := <-firstCh
	if first.err != nil {
		t.Fatalf("first refresh error: %v", first.err)
	}
	if !first.refreshed {
		t.Fatal("first refresh refreshed = false, want true")
	}
	second := <-secondCh
	if second.err != nil {
		t.Fatalf("second refresh error: %v", second.err)
	}
	if second.refreshed {
		t.Fatal("second refresh refreshed = true, want shared in-flight result")
	}
	if !second.runtimeAt.Equal(first.runtimeAt) {
		t.Fatalf("second runtimeAt = %v, want shared %v", second.runtimeAt, first.runtimeAt)
	}
	if got := tmuxRunner.listSessionCallCount(); got != 1 {
		t.Fatalf("tmux list-sessions calls = %d, want one shared refresh", got)
	}
}

func TestHandleTaskListIgnoresAgentPaneStatusForTaskLifecycle(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	projectID := "proj-agent-task-list"
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "paused agent in live tmux",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	startedAt := time.Date(2026, time.May, 29, 8, 0, 0, 0, time.UTC)
	containerID := naming.CanonicalSessionID(projectID, taskID)
	if err := runtimeStateStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID:                containerID,
		IssueID:           taskID,
		State:             daemonstate.SessionStateAttached,
		ObservedState:     daemonstate.SessionStateAttached,
		TmuxAttachedCount: 1,
		StartedAt:         &startedAt,
		UpdatedAt:         startedAt,
	}); err != nil {
		t.Fatalf("seed container session: %v", err)
	}
	if err := runtimeStateStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID:            containerID + ".pane-190",
		IssueID:       taskID,
		State:         daemonstate.SessionStatePaused,
		ObservedState: daemonstate.SessionStatePaused,
		StartedAt:     &startedAt,
		UpdatedAt:     startedAt.Add(time.Minute),
	}); err != nil {
		t.Fatalf("seed paused pane session: %v", err)
	}

	d := &Daemon{
		cfg: Config{
			RepoDir: ".",
			Logger:  logger,
		},
		issues: issuesClient,
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		sessionStore: daemonstate.NewStore(),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
		revision: map[string]uint64{projectID: 9},
	}

	resp, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-list-agent-status",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.list",
	})
	if err != nil {
		t.Fatalf("handleTaskList error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.list response = %+v", resp.Error)
	}
	var payload protocol.TaskListSnapshotPayload
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("unmarshal task list body: %v", err)
	}
	if len(payload.Tasks) != 1 || payload.Tasks[0].Session == nil {
		t.Fatalf("tasks = %+v, want one task with session", payload.Tasks)
	}
	session := payload.Tasks[0].Session
	if session.State != domain.SessionPaused {
		t.Fatalf("session state = %s, want %s", session.State, domain.SessionPaused)
	}
	if session.TotalCount != 1 || session.ActiveCount != 0 || session.PausedCount != 1 {
		t.Fatalf("session counts = %d/%d/%d, want 1/0/1", session.TotalCount, session.ActiveCount, session.PausedCount)
	}
	if !session.TmuxAttached || session.TmuxAttachedCount != 1 {
		t.Fatalf("tmux attachment = %v/%d, want true/1", session.TmuxAttached, session.TmuxAttachedCount)
	}
}

func TestHandleTaskGetUsesFreshTaskListSnapshotCache(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-cache-get"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "cached issue",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	d := &Daemon{
		cfg: Config{Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 3},
		hub:      publish.NewHub(16, 8, logger),
	}

	d.storeTaskListSnapshotCache(projectID, 3, time.Now().UTC(), protocol.TaskListFreshnessFresh, []domain.Task{{
		ID:       naming.IssueID(taskID),
		Title:    "cached issue",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	}}, false)

	if err := issuesClient.Update(ctx, taskID, domain.StatusInReview); err != nil {
		t.Fatalf("update issue behind cache: %v", err)
	}

	body, err := json.Marshal(map[string]string{"task_id": taskID})
	if err != nil {
		t.Fatalf("marshal task get request: %v", err)
	}
	getResp, err := d.handleTaskGet(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-get-cache-hit",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.get",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskGet error: %v", err)
	}
	if !getResp.OK {
		t.Fatalf("task.get response = %+v", getResp.Error)
	}

	payload, err := protocol.DecodeTaskListSnapshotPayload(getResp.Body)
	if err != nil {
		t.Fatalf("decode task.get body: %v", err)
	}
	if got, want := payload.SnapshotRevision, uint64(3); got != want {
		t.Fatalf("payload.SnapshotRevision = %d, want %d", got, want)
	}
	if got, want := len(payload.Tasks), 1; got != want {
		t.Fatalf("payload.Tasks len = %d, want %d", got, want)
	}
	if got, want := payload.Tasks[0].Status, domain.StatusOpen; got != want {
		t.Fatalf("payload task status = %q, want cached %q", got, want)
	}
}

func TestHandleTaskGetComposesRuntimeProjectionOverCachedTaskSnapshot(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-task-get-worktree-refresh"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "stale dirty worktree issue",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	worktreePath := filepath.Join(t.TempDir(), "Chefy-"+taskID)
	branch := "riordanpawley/" + taskID + "/stale-dirty"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      worktreePath,
		Branch:    branch,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}
	staleDirtyStatus := git.GitStatus{
		HasChanges:     true,
		GitAdditions:   2154,
		GitDeletions:   1400,
		GitAheadCount:  4,
		GitBehindCount: 25,
	}
	staleDirtyStatusRaw, err := json.Marshal(staleDirtyStatus)
	if err != nil {
		t.Fatalf("marshal stale status: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeStateGitStatus(ctx, projectID, taskID, staleDirtyStatusRaw, time.Now().Add(-time.Hour).UTC()); err != nil {
		t.Fatalf("seed stale dirty git status: %v", err)
	}

	d := &Daemon{
		cfg: Config{BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		revision: map[string]uint64{projectID: 12},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.storeTaskListSnapshotCache(projectID, 12, time.Now().UTC(), protocol.TaskListFreshnessFresh, []domain.Task{{
		ID:                    naming.IssueID(taskID),
		Title:                 "cached durable issue",
		Type:                  domain.TypeTask,
		Priority:              domain.P2,
		Status:                domain.StatusInProgress,
		HasWorktree:           true,
		HasUncommittedChanges: true,
		GitAdditions:          2154,
		GitDeletions:          1400,
		GitAheadCount:         4,
		GitBehindCount:        25,
	}}, false)
	if cached, ok := d.readFreshTaskListSnapshotCache(projectID); !ok {
		t.Fatal("task snapshot cache should be fresh")
	} else if cached.Tasks[0].HasUncommittedChanges || cached.Tasks[0].GitAdditions != 0 || cached.Tasks[0].GitDeletions != 0 {
		t.Fatalf("cached task retained runtime fields: %+v", cached.Tasks[0])
	}
	cleanStatusRaw, err := json.Marshal(git.GitStatus{
		HasChanges:     false,
		GitAdditions:   12,
		GitDeletions:   4,
		GitAheadCount:  4,
		GitBehindCount: 25,
	})
	if err != nil {
		t.Fatalf("marshal clean status: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeStateGitStatus(ctx, projectID, taskID, cleanStatusRaw, time.Now().UTC()); err != nil {
		t.Fatalf("persist refreshed clean git status: %v", err)
	}

	body, err := json.Marshal(map[string]string{"task_id": taskID})
	if err != nil {
		t.Fatalf("marshal task get request: %v", err)
	}
	resp, err := d.handleTaskGet(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-get-refresh-stale-dirty",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.get",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskGet error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.get response = %+v", resp.Error)
	}

	payload, err := protocol.DecodeTaskListSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatalf("decode task.get body: %v", err)
	}
	if got, want := len(payload.Tasks), 1; got != want {
		t.Fatalf("payload.Tasks len = %d, want %d", got, want)
	}
	task := payload.Tasks[0]
	if !task.HasWorktree {
		t.Fatal("task.HasWorktree = false, want true")
	}
	if got, want := task.Title, "cached durable issue"; got != want {
		t.Fatalf("task.Title = %q, want cached durable title %q", got, want)
	}
	if task.HasUncommittedChanges {
		t.Fatalf("task.HasUncommittedChanges = true, want false: %+v", task)
	}
	if got, want := task.GitAdditions, 12; got != want {
		t.Fatalf("task.GitAdditions = %d, want refreshed %d", got, want)
	}
	if got, want := task.GitDeletions, 4; got != want {
		t.Fatalf("task.GitDeletions = %d, want refreshed %d", got, want)
	}
	if got, want := task.GitAheadCount, 4; got != want {
		t.Fatalf("task.GitAheadCount = %d, want refreshed %d", got, want)
	}
	if got, want := task.GitBehindCount, 25; got != want {
		t.Fatalf("task.GitBehindCount = %d, want refreshed %d", got, want)
	}
}

func TestHandleTaskListIgnoresFreshCacheAndReadsSQLiteProjection(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-cache-list"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "current sqlite list",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	d := &Daemon{
		cfg: Config{Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeAdapter: &worktreeServiceAdapter{},
		revision:        map[string]uint64{projectID: 11},
		hub:             publish.NewHub(16, 8, logger),
	}
	cachedTask := domain.Task{
		ID:       naming.IssueID(taskID),
		Title:    "stale cached list",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusDone,
	}
	d.storeTaskListSnapshotCache(projectID, 11, time.Now().UTC(), protocol.TaskListFreshnessFresh, []domain.Task{cachedTask}, false)

	resp, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-list-cache-hit",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.list",
	})
	if err != nil {
		t.Fatalf("handleTaskList error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.list response = %+v", resp.Error)
	}

	payload, err := protocol.DecodeTaskListSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatalf("decode task.list body: %v", err)
	}
	if got, want := payload.SnapshotRevision, uint64(11); got != want {
		t.Fatalf("payload.SnapshotRevision = %d, want %d", got, want)
	}
	if got, want := len(payload.Tasks), 1; got != want {
		t.Fatalf("payload.Tasks len = %d, want %d", got, want)
	}
	if got, want := payload.Tasks[0].Title, "current sqlite list"; got != want {
		t.Fatalf("payload task title = %q, want sqlite %q", got, want)
	}
	if got, want := payload.Tasks[0].Status, domain.StatusOpen; got != want {
		t.Fatalf("payload task status = %q, want sqlite %q", got, want)
	}

	d.worktreeStateRefreshMu.Lock()
	defer d.worktreeStateRefreshMu.Unlock()
	if got := d.worktreeStateLastRefresh[projectID]; got.IsZero() {
		t.Fatal("worktree refresh was not triggered on task.list sqlite projection read")
	}
}

func TestHandleBoardFetchComposesRuntimeProjectionOverCachedTaskSnapshot(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-cache-board-runtime-overlay"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "sqlite title should not replace durable cache",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	worktreePath := filepath.Join(t.TempDir(), "Chefy-"+taskID)
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      worktreePath,
		Branch:    "az/" + taskID,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}
	statusRaw, err := json.Marshal(git.GitStatus{
		HasChanges:    true,
		GitAdditions:  7,
		GitDeletions:  2,
		GitAheadCount: 3,
	})
	if err != nil {
		t.Fatalf("marshal git status: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeStateGitStatus(ctx, projectID, taskID, statusRaw, time.Now().UTC()); err != nil {
		t.Fatalf("seed git status projection: %v", err)
	}

	d := &Daemon{
		cfg: Config{Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeAdapter: &worktreeServiceAdapter{},
		revision:        map[string]uint64{projectID: 11},
		hub:             publish.NewHub(16, 8, logger),
	}
	d.storeTaskListSnapshotCache(projectID, 11, time.Now().UTC(), protocol.TaskListFreshnessFresh, []domain.Task{{
		ID:                    naming.IssueID(taskID),
		Title:                 "cached durable title",
		Type:                  domain.TypeTask,
		Priority:              domain.P2,
		Status:                domain.StatusOpen,
		HasWorktree:           false,
		HasUncommittedChanges: false,
	}}, true)

	resp, err := d.handleBoardFetch(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-board-fetch-runtime-overlay",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "board.fetch",
	})
	if err != nil {
		t.Fatalf("handleBoardFetch error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("board.fetch response = %+v", resp.Error)
	}
	payload, err := protocol.DecodeBoardSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatalf("decode board.fetch body: %v", err)
	}
	if got, want := len(payload.Tasks), 1; got != want {
		t.Fatalf("payload.Tasks len = %d, want %d", got, want)
	}
	task := payload.Tasks[0]
	if got, want := task.Title, "cached durable title"; got != want {
		t.Fatalf("task.Title = %q, want cached durable title %q", got, want)
	}
	if !task.HasWorktree || !task.HasUncommittedChanges || task.GitAdditions != 7 || task.GitDeletions != 2 || task.GitAheadCount != 3 {
		t.Fatalf("task runtime overlay = %+v, want dirty projected worktree", task)
	}
}

func TestHandleTaskListReadsSQLiteProjection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	logger := slog.Default()
	projectID := "proj-local-first-list"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "foreground reads local projection",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	d := &Daemon{
		cfg: Config{Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 17},
	}

	resp, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-local-first-list",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.list",
	})
	if err != nil {
		t.Fatalf("handleTaskList error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.list response = %+v", resp.Error)
	}
	payload, err := protocol.DecodeTaskListSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatalf("decode task.list body: %v", err)
	}
	if got, want := len(payload.Tasks), 1; got != want {
		t.Fatalf("task count = %d, want %d", got, want)
	}
	if payload.Tasks[0].ID.String() != taskID || payload.Tasks[0].Title != "foreground reads local projection" {
		t.Fatalf("payload task = %+v, want local issue %s", payload.Tasks[0], taskID)
	}
}

func TestLoadTaskListSnapshotSharesInFlightProjectLoad(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	projectID := "proj-shared-list"
	done := make(chan struct{})
	d := &Daemon{
		taskListSnapshotLoads: map[string]*taskListSnapshotLoad{
			projectID: {done: done},
		},
	}

	resultCh := make(chan taskListSnapshotLoadResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, shared, err := d.loadTaskListSnapshot(ctx, protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-shared-list",
			Kind:            protocol.EnvelopeKindCommand,
			Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
			Command:         "task.list",
		}, projectID, "", false)
		if !shared {
			errCh <- errors.New("load was not shared")
			return
		}
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	d.taskListSnapshotLoadMu.Lock()
	inflight := d.taskListSnapshotLoads[projectID]
	inflight.result = taskListSnapshotLoadResult{
		Revision:      17,
		LastCheckedAt: time.Now().UTC(),
		Freshness:     protocol.TaskListFreshnessFresh,
		Tasks: []domain.Task{{
			ID:     "az-shared",
			Title:  "shared result",
			Status: domain.StatusOpen,
		}},
	}
	close(done)
	d.taskListSnapshotLoadMu.Unlock()

	select {
	case err := <-errCh:
		t.Fatalf("shared load error: %v", err)
	case result := <-resultCh:
		if got, want := result.Revision, uint64(17); got != want {
			t.Fatalf("result.Revision = %d, want %d", got, want)
		}
		if got, want := len(result.Tasks), 1; got != want {
			t.Fatalf("result.Tasks len = %d, want %d", got, want)
		}
		result.Tasks[0].Title = "mutated"
		if got := d.taskListSnapshotLoads[projectID].result.Tasks[0].Title; got != "shared result" {
			t.Fatalf("shared result was not cloned; title = %q", got)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for shared task-list load")
	}
}

func TestLoadTaskListSnapshotUsesDetachedBuildContext(t *testing.T) {
	logger := slog.Default()
	projectID := "proj-canceled-owner-list"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	taskID, err := issuesClient.Create(context.Background(), issues.CreateTaskParams{
		Title:    "canceled owner should not poison load",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	d := &Daemon{
		cfg: Config{Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 23},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, shared, err := d.loadTaskListSnapshot(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-canceled-owner-list",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.list",
	}, projectID, "", false)
	if err != nil {
		t.Fatalf("loadTaskListSnapshot error = %v, want detached load to succeed", err)
	}
	if shared {
		t.Fatal("shared = true, want owner load")
	}
	if got, want := result.Revision, uint64(23); got != want {
		t.Fatalf("result.Revision = %d, want %d", got, want)
	}
	if len(result.Tasks) != 1 || result.Tasks[0].ID.String() != taskID {
		t.Fatalf("result.Tasks = %+v, want task %s", result.Tasks, taskID)
	}
}

func TestHandleTaskListAppliesContentQueryInDaemon(t *testing.T) {
	logger := slog.Default()
	projectID := "proj-query-list"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	matchID, err := issuesClient.Create(context.Background(), issues.CreateTaskParams{
		Title:       "Alpha",
		Description: "Contains runtime cache evidence",
		Type:        domain.TypeTask,
		Priority:    domain.P2,
		Status:      domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create matching issue: %v", err)
	}
	if _, err := issuesClient.Create(context.Background(), issues.CreateTaskParams{
		Title:       "Beta",
		Description: "Unrelated",
		Type:        domain.TypeTask,
		Priority:    domain.P2,
		Status:      domain.StatusOpen,
	}); err != nil {
		t.Fatalf("create nonmatching issue: %v", err)
	}

	d := &Daemon{
		cfg: Config{Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 23},
	}
	body, err := json.Marshal(protocol.TaskListRequestBody{Query: "RUNTIME cache"})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := d.handleTaskList(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-query-list",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.list",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskList error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("handleTaskList response = %+v", resp.Error)
	}
	payload, err := protocol.DecodeTaskListSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatalf("decode task list body: %v", err)
	}
	if payload.SummariesOnly {
		t.Fatal("query task list should return full task payloads, got summaries_only")
	}
	if len(payload.Tasks) != 1 || payload.Tasks[0].ID.String() != matchID {
		t.Fatalf("payload tasks = %+v, want only %s", payload.Tasks, matchID)
	}
	if payload.Tasks[0].Description == "" {
		t.Fatalf("query task list lost full issue content: %+v", payload.Tasks[0])
	}
}

func TestHandleTaskListIncludesDependenciesOnlyWhenRequested(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-list-deps"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Parent",
		Type:     domain.TypeEpic,
		Priority: domain.P1,
		Status:   domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create parent issue: %v", err)
	}
	blockerID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Blocker",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create blocker issue: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Child",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusOpen,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child issue: %v", err)
	}
	if err := issuesClient.AddDependency(ctx, childID, blockerID, string(domain.DependencyBlocks)); err != nil {
		t.Fatalf("add blocker dependency: %v", err)
	}

	d := &Daemon{
		cfg: Config{Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 4},
	}

	decodeChild := func(t *testing.T, body []byte) domain.Task {
		t.Helper()
		payload, err := protocol.DecodeTaskListSnapshotPayload(body)
		if err != nil {
			t.Fatalf("decode task list body: %v", err)
		}
		for _, task := range payload.Tasks {
			if task.ID.String() == childID {
				return task
			}
		}
		t.Fatalf("child %s missing from payload: %+v", childID, payload.Tasks)
		return domain.Task{}
	}

	defaultResp, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-list-deps-default",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.list",
	})
	if err != nil {
		t.Fatalf("handle default task.list error: %v", err)
	}
	if !defaultResp.OK {
		t.Fatalf("default task.list response = %+v", defaultResp.Error)
	}
	defaultTask := decodeChild(t, defaultResp.Body)
	if defaultTask.ParentID == nil || defaultTask.ParentID.String() != parentID {
		t.Fatalf("default parent = %v, want %s", defaultTask.ParentID, parentID)
	}
	if len(defaultTask.Dependencies) != 0 {
		t.Fatalf("default dependencies = %+v, want none", defaultTask.Dependencies)
	}

	body, err := json.Marshal(protocol.TaskListRequestBody{IncludeDependencies: true})
	if err != nil {
		t.Fatalf("marshal dependency request: %v", err)
	}
	fullResp, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-list-deps-full",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.list",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handle full-dependency task.list error: %v", err)
	}
	if !fullResp.OK {
		t.Fatalf("full-dependency task.list response = %+v", fullResp.Error)
	}
	fullTask := decodeChild(t, fullResp.Body)
	if fullTask.ParentID == nil || fullTask.ParentID.String() != parentID {
		t.Fatalf("full parent = %v, want %s", fullTask.ParentID, parentID)
	}
	if len(fullTask.Dependencies) != 1 || fullTask.Dependencies[0].ID.String() != blockerID {
		t.Fatalf("full dependencies = %+v, want blocker %s", fullTask.Dependencies, blockerID)
	}

	boardResp, err := d.handleBoardFetch(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-list-deps-board",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "board.fetch",
	})
	if err != nil {
		t.Fatalf("handle board.fetch error: %v", err)
	}
	if !boardResp.OK {
		t.Fatalf("board.fetch response = %+v", boardResp.Error)
	}
	boardPayload, err := protocol.DecodeBoardSnapshotPayload(boardResp.Body)
	if err != nil {
		t.Fatalf("decode board body: %v", err)
	}
	foundChild := false
	for _, task := range boardPayload.Tasks {
		if task.ID.String() != childID {
			continue
		}
		foundChild = true
		if len(task.Dependencies) != 0 {
			t.Fatalf("board child dependencies = %+v, want none", task.Dependencies)
		}
	}
	if !foundChild {
		t.Fatalf("board payload missing child %s: %+v", childID, boardPayload.Tasks)
	}
}

func TestHandleTaskGetInvalidatesTaskListSnapshotCacheAfterIssueUpdate(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-cache-invalidation"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "cache invalidates",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	d := &Daemon{
		cfg: Config{Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 3},
		hub:      publish.NewHub(16, 8, logger),
	}

	listResp, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-list-cache-prime",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.list",
	})
	if err != nil {
		t.Fatalf("handleTaskList error: %v", err)
	}
	if !listResp.OK {
		t.Fatalf("task.list response = %+v", listResp.Error)
	}

	updateBody, err := json.Marshal(map[string]any{
		"task_id": taskID,
		"status":  domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("marshal task update request: %v", err)
	}
	updateResp, err := d.handleTaskUpdateStatus(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-update-cache-invalidate",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.update_status",
		Body:            updateBody,
	})
	if err != nil {
		t.Fatalf("handleTaskUpdateStatus error: %v", err)
	}
	if !updateResp.OK {
		t.Fatalf("task.update_status response = %+v", updateResp.Error)
	}

	getBody, err := json.Marshal(map[string]string{"task_id": taskID})
	if err != nil {
		t.Fatalf("marshal task get request: %v", err)
	}
	getResp, err := d.handleTaskGet(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-get-after-update",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.get",
		Body:            getBody,
	})
	if err != nil {
		t.Fatalf("handleTaskGet error: %v", err)
	}
	if !getResp.OK {
		t.Fatalf("task.get response = %+v", getResp.Error)
	}
	payload, err := protocol.DecodeTaskListSnapshotPayload(getResp.Body)
	if err != nil {
		t.Fatalf("decode task.get body: %v", err)
	}
	if got, want := payload.SnapshotRevision, uint64(4); got != want {
		t.Fatalf("payload.SnapshotRevision = %d, want %d", got, want)
	}
	if got, want := len(payload.Tasks), 1; got != want {
		t.Fatalf("payload.Tasks len = %d, want %d", got, want)
	}
	if got, want := payload.Tasks[0].Status, domain.StatusInReview; got != want {
		t.Fatalf("payload task status = %q, want %q", got, want)
	}
}

func TestTaskUpdateStatusRejectsRawCloseRuntimeAttachments(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-guard-runtime"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Runtime guard",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      "/tmp/" + taskID,
		Branch:    "riordan/" + taskID,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}

	body, err := json.Marshal(map[string]any{
		"task_id": taskID,
		"status":  domain.StatusDone,
	})
	if err != nil {
		t.Fatalf("marshal task update request: %v", err)
	}
	resp, err := d.handleTaskUpdateStatus(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-guard-runtime",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.update_status",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskUpdateStatus error: %v", err)
	}
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "status closed must be applied with task.close") {
		t.Fatalf("task.update_status response = %+v, want raw close rejection", resp)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get guarded issue after failed close: %v", err)
	}
	if task.Status != domain.StatusInReview {
		t.Fatalf("guarded issue status = %s, want %s", task.Status, domain.StatusInReview)
	}

}

func TestTaskClosePreflightBlocksDirtyWorktreeInDaemonPolicy(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-guard-dirty"
	repoDir := filepath.Join(t.TempDir(), "repo")
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Dirty preflight",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	worktreePath := filepath.Join(t.TempDir(), "repo-"+taskID)
	branchName := "riordan/" + taskID + "/work"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      worktreePath,
		Branch:    branchName,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "worktree list --porcelain"):
			return fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", worktreePath, branchName), nil
		case strings.Contains(joined, "status --porcelain"):
			return " M main.go\n", nil
		default:
			return "", nil
		}
	}}
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: logger, BaseBranch: "main"},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(runner, repoDir, logger),
		},
		gitStatusAdapter: &gitServiceAdapter{
			client:            git.NewClient(runner, logger),
			runtimeStateStore: runtimeStore,
			logger:            logger,
			baseBranch:        "main",
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}

	body, err := json.Marshal(map[string]any{
		"task_id":               taskID,
		"allow_target_worktree": true,
	})
	if err != nil {
		t.Fatalf("marshal close preflight request: %v", err)
	}
	resp, err := d.handleTaskClosePreflight(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-guard-dirty",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close_preflight",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClosePreflight error: %v", err)
	}
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "worktree has local changes: main.go") || !strings.Contains(resp.Error.Message, "commit, discard, or merge the worktree changes first") {
		t.Fatalf("task.close_preflight response = %+v, want dirty worktree guard", resp)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get guarded issue after failed preflight: %v", err)
	}
	if task.Status != domain.StatusInReview {
		t.Fatalf("guarded issue status = %s, want %s", task.Status, domain.StatusInReview)
	}
}

func TestTaskCloseBlocksActiveSessionActivity(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()

	tests := []struct {
		name           string
		activity       string
		activitySource string
		wantActivity   string
	}{
		{name: "busy", activity: "busy", activitySource: "hooks", wantActivity: "busy"},
		{name: "waiting", activity: "waiting", activitySource: "hooks", wantActivity: "waiting"},
		{name: "working", activity: "working", activitySource: "hooks", wantActivity: "working"},
		{name: "unknown", activity: "unknown", activitySource: "none", wantActivity: "unknown"},
		{name: "no agent", activity: "no-agent", activitySource: "session", wantActivity: "no-agent"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectID := "proj-close-active-" + strings.ReplaceAll(tt.name, " ", "-")
			issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
			issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
			t.Cleanup(func() { _ = issuesClient.CloseDB() })
			runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
			t.Cleanup(func() { _ = runtimeStore.Close() })

			taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
				Title:    "Close active session",
				Type:     domain.TypeTask,
				Priority: domain.P2,
				Status:   domain.StatusInReview,
			})
			if err != nil {
				t.Fatalf("create issue: %v", err)
			}
			sessionID := naming.CanonicalSessionID(projectID, taskID)
			if err := runtimeStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
				ID:             sessionID,
				IssueID:        taskID,
				State:          daemonstate.SessionStateRunning,
				ObservedState:  daemonstate.SessionStateRunning,
				Activity:       tt.activity,
				ActivitySource: tt.activitySource,
				UpdatedAt:      time.Now().UTC(),
			}); err != nil {
				t.Fatalf("seed session projection: %v", err)
			}

			d := &Daemon{
				cfg:          Config{RepoDir: ".", Logger: logger},
				sessionStore: daemonstate.NewStore(),
				issueClientsByProject: map[string]*issues.Client{
					projectID: issuesClient,
				},
				runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
					projectID: runtimeStore,
				},
				revision: map[string]uint64{projectID: 1},
				hub:      publish.NewHub(16, 8, logger),
			}

			body, err := json.Marshal(taskCloseRequest{TaskID: taskID})
			if err != nil {
				t.Fatalf("marshal close request: %v", err)
			}
			resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
				ProtocolVersion: protocol.CurrentVersion,
				RequestID:       "req-close-active-session",
				Kind:            protocol.EnvelopeKindCommand,
				Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
				Command:         "task.close",
				Body:            body,
			})
			if err != nil {
				t.Fatalf("handleTaskClose error: %v", err)
			}
			if resp.OK || resp.Error == nil {
				t.Fatalf("task.close response = %+v, want session activity guard", resp)
			}
			for _, want := range []string{
				"session activity is " + tt.wantActivity,
				"wait for the session projection to report idle/done/terminal activity or intentionally stop the session",
			} {
				if !strings.Contains(resp.Error.Message, want) {
					t.Fatalf("task.close error = %q, want %q", resp.Error.Message, want)
				}
			}
			task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
			if err != nil {
				t.Fatalf("get issue after blocked close: %v", err)
			}
			if task.Status != domain.StatusInReview {
				t.Fatalf("task status = %s, want %s", task.Status, domain.StatusInReview)
			}
			session, found, err := runtimeStore.GetSessionState(ctx, projectID, sessionID)
			if err != nil {
				t.Fatalf("get session projection: %v", err)
			}
			if !found || session.State != daemonstate.SessionStateRunning {
				t.Fatalf("session projection = %+v found=%t, want running row preserved", session, found)
			}
		})
	}
}

func TestTaskCloseAllowsExplicitActiveSessionOverride(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-active-override"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Close active session with override",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	sessionID := naming.CanonicalSessionID(projectID, taskID)
	if err := runtimeStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID:             sessionID,
		IssueID:        taskID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "busy",
		ActivitySource: "hooks",
		UpdatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed session projection: %v", err)
	}

	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: logger},
		sessionStore: daemonstate.NewStore(),
		tmux:         tmux.NewClient(&testTmuxRunner{sessions: map[string]bool{}}, logger),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}

	body, err := json.Marshal(taskCloseRequest{TaskID: taskID, AllowActiveSession: true})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-active-session-override",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.close response = %+v, want success", resp)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get closed issue: %v", err)
	}
	if task.Status != domain.StatusDone {
		t.Fatalf("task status = %s, want %s", task.Status, domain.StatusDone)
	}
	session, found, err := runtimeStore.GetSessionState(ctx, projectID, sessionID)
	if err != nil {
		t.Fatalf("get session projection: %v", err)
	}
	if !found || session.State != daemonstate.SessionStateStopped || session.ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("session projection = %+v found=%t, want stopped row", session, found)
	}
}

func TestTaskCloseAllowsTerminalOrIdleSessionActivity(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()

	tests := []struct {
		name           string
		activity       string
		activitySource string
	}{
		{name: "idle", activity: "idle", activitySource: "hooks"},
		{name: "done", activity: "done", activitySource: "hooks"},
		{name: "error", activity: "error", activitySource: "hooks"},
		{name: "paused", activity: "paused", activitySource: "session"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectID := "proj-close-inactive-" + tt.name
			issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
			issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
			t.Cleanup(func() { _ = issuesClient.CloseDB() })
			runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
			t.Cleanup(func() { _ = runtimeStore.Close() })

			taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
				Title:    "Close inactive session",
				Type:     domain.TypeTask,
				Priority: domain.P2,
				Status:   domain.StatusInReview,
			})
			if err != nil {
				t.Fatalf("create issue: %v", err)
			}
			sessionID := naming.CanonicalSessionID(projectID, taskID)
			if err := runtimeStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
				ID:             sessionID,
				IssueID:        taskID,
				State:          daemonstate.SessionStateRunning,
				ObservedState:  daemonstate.SessionStateRunning,
				Activity:       tt.activity,
				ActivitySource: tt.activitySource,
				UpdatedAt:      time.Now().UTC(),
			}); err != nil {
				t.Fatalf("seed session projection: %v", err)
			}

			d := &Daemon{
				cfg:          Config{RepoDir: ".", Logger: logger},
				sessionStore: daemonstate.NewStore(),
				tmux:         tmux.NewClient(&testTmuxRunner{sessions: map[string]bool{}}, logger),
				issueClientsByProject: map[string]*issues.Client{
					projectID: issuesClient,
				},
				runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
					projectID: runtimeStore,
				},
				revision: map[string]uint64{projectID: 1},
				hub:      publish.NewHub(16, 8, logger),
			}

			body, err := json.Marshal(taskCloseRequest{TaskID: taskID})
			if err != nil {
				t.Fatalf("marshal close request: %v", err)
			}
			resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
				ProtocolVersion: protocol.CurrentVersion,
				RequestID:       "req-close-inactive-session",
				Kind:            protocol.EnvelopeKindCommand,
				Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
				Command:         "task.close",
				Body:            body,
			})
			if err != nil {
				t.Fatalf("handleTaskClose error: %v", err)
			}
			if !resp.OK {
				t.Fatalf("task.close response = %+v, want success", resp)
			}
			task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
			if err != nil {
				t.Fatalf("get closed issue: %v", err)
			}
			if task.Status != domain.StatusDone {
				t.Fatalf("task status = %s, want %s", task.Status, domain.StatusDone)
			}
			session, found, err := runtimeStore.GetSessionState(ctx, projectID, sessionID)
			if err != nil {
				t.Fatalf("get session projection: %v", err)
			}
			if !found || session.State != daemonstate.SessionStateStopped || session.ObservedState != daemonstate.SessionStateStopped {
				t.Fatalf("session projection = %+v found=%t, want stopped row", session, found)
			}
		})
	}
}

func TestTaskCloseCommandUpdatesStatusThroughDaemon(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-task-close"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Close me",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := issuesClient.Update(ctx, taskID, domain.StatusInReview); err != nil {
		t.Fatalf("update task: %v", err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		hub: publish.NewHub(16, 8, slog.Default()),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
		revision: map[string]uint64{},
	}
	body, err := json.Marshal(taskCloseRequest{TaskID: taskID})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.close",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("handleTaskClose response = %+v", resp)
	}
	var result taskCloseResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal close response: %v", err)
	}
	if result.TaskID != taskID || result.Status != string(domain.StatusDone) || result.Revision == 0 {
		t.Fatalf("close result = %+v", result)
	}
	if got := taskClosePhaseNames(result.Phases); !slices.Contains(got, "preflight") || !slices.Contains(got, "status_write") {
		t.Fatalf("close phases = %v, want preflight and status_write", got)
	}
	tasks, err := issuesClient.List(ctx)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	var task domain.Task
	found := false
	for _, candidate := range tasks {
		if candidate.ID.String() == taskID {
			task = candidate
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("task not found after close: %s", taskID)
	}
	if task.Status != domain.StatusDone {
		t.Fatalf("task status = %s, want done", task.Status)
	}
}

func TestTaskCloseBlocksUnresolvedChildrenByDefault(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-task-close-child-default"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Parent", Type: domain.TypeTask, Status: domain.StatusInReview})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Child", Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		hub: publish.NewHub(16, 8, slog.Default()),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
		revision: map[string]uint64{},
	}
	body, err := json.Marshal(taskCloseRequest{TaskID: parentID})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-child-default",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.close",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "unresolved child issues remain: "+childID+" (open)") {
		t.Fatalf("handleTaskClose response = %+v, want unresolved child guard", resp)
	}
}

func TestTaskCloseCanAutoCloseCleanUnresolvedChildren(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-task-close-clean-children"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Parent", Type: domain.TypeTask, Status: domain.StatusInReview})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Child", Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		hub: publish.NewHub(16, 8, slog.Default()),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
		revision: map[string]uint64{},
	}
	body, err := json.Marshal(taskCloseRequest{TaskID: parentID, CloseCleanChildren: true})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-clean-children",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.close",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("handleTaskClose response = %+v", resp)
	}
	var result taskCloseResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal close response: %v", err)
	}
	if !slices.Contains(result.AutoClosedChildren, childID) {
		t.Fatalf("auto closed children = %v, want %s", result.AutoClosedChildren, childID)
	}
	for _, issueID := range []string{parentID, childID} {
		task, err := issuesClient.GetWithRuntime(ctx, projectID, issueID)
		if err != nil {
			t.Fatalf("get %s after close: %v", issueID, err)
		}
		if task.Status != domain.StatusDone {
			t.Fatalf("%s status = %s, want %s", issueID, task.Status, domain.StatusDone)
		}
	}
}

func TestTaskCloseCleanChildrenDoesNotForceDirtyChildWorktree(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-task-close-clean-child-dirty"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Parent", Type: domain.TypeTask, Status: domain.StatusInReview})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Child", Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = store.Close() })
	childWorktree := filepath.Join(repoDir, "wt-"+childID)
	childBranch := "riordan/" + childID + "/work"
	if err := store.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   childID,
		Path:      childWorktree,
		Branch:    childBranch,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed child worktree: %v", err)
	}
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "worktree list --porcelain"):
			return fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", childWorktree, childBranch), nil
		case strings.Contains(joined, "status --porcelain"):
			return " M child.go\n", nil
		default:
			return "", nil
		}
	}}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: logger, BaseBranch: "main"},
		hub: publish.NewHub(16, 8, logger),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		gitStatusAdapter: &gitServiceAdapter{
			client:            git.NewClient(runner, logger),
			runtimeStateStore: store,
			logger:            logger,
			baseBranch:        "main",
		},
		revision: map[string]uint64{},
	}
	body, err := json.Marshal(taskCloseRequest{
		TaskID:             parentID,
		ForceWorktree:      true,
		CloseCleanChildren: true,
	})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-clean-child-dirty",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.close",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "worktree has local changes: child.go") {
		t.Fatalf("handleTaskClose response = %+v, want dirty child worktree guard", resp)
	}
	child, err := issuesClient.GetWithRuntime(ctx, projectID, childID)
	if err != nil {
		t.Fatalf("get child after blocked close: %v", err)
	}
	if child.Status != domain.StatusOpen {
		t.Fatalf("child status = %s, want %s", child.Status, domain.StatusOpen)
	}
}

func taskClosePhaseNames(phases []taskClosePhaseTiming) []string {
	names := make([]string, 0, len(phases))
	for _, phase := range phases {
		names = append(names, phase.Name)
	}
	return names
}

func taskClosePhaseByName(phases []taskClosePhaseTiming, name string) (taskClosePhaseTiming, bool) {
	for _, phase := range phases {
		if phase.Name == name {
			return phase, true
		}
	}
	return taskClosePhaseTiming{}, false
}

func TestTaskCloseRepairsLegacyProjectRuntimeProjectionBeforeFinalStatusUpdate(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-current"
	legacyProjectID := "proj-close-legacy"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Close with legacy projection alias",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: legacyProjectID,
		IssueID:   taskID,
		Path:      filepath.Join(t.TempDir(), "missing", taskID),
		Branch:    "riordan/" + taskID + "/legacy",
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed legacy worktree projection: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger, BaseBranch: "main"},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}

	body, err := json.Marshal(taskCloseRequest{TaskID: taskID})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-legacy-projection",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.close response = %+v, want success", resp)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get closed issue: %v", err)
	}
	if task.Status != domain.StatusDone {
		t.Fatalf("task status = %s, want %s", task.Status, domain.StatusDone)
	}
	if _, found, err := runtimeStore.GetWorktreeStateByIssueID(ctx, legacyProjectID, taskID); err != nil {
		t.Fatalf("get legacy worktree projection: %v", err)
	} else if found {
		t.Fatalf("legacy worktree projection still present for %s", taskID)
	}
}

func TestTaskCloseRepairsVerifiedStaleLegacyProjectSessionProjection(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-current"
	legacyProjectID := "proj-close-legacy-session-stale"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Close with stale legacy session alias",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	sessionID := "legacy-" + taskID
	if err := runtimeStore.UpsertSessionState(ctx, legacyProjectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   taskID,
		State:     daemonstate.SessionStateRunning,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed stale legacy session projection: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger, BaseBranch: "main"},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		tmux:     tmux.NewClient(&recordingGitRunner{runFn: func(args ...string) (string, error) { return "", nil }}, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}

	body, err := json.Marshal(taskCloseRequest{TaskID: taskID})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-stale-legacy-session",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.close response = %+v, want success", resp)
	}
	session, found, err := runtimeStore.GetSessionState(ctx, legacyProjectID, sessionID)
	if err != nil {
		t.Fatalf("get stale legacy session projection: %v", err)
	}
	if !found {
		t.Fatalf("stale legacy session projection missing for %s", sessionID)
	}
	if session.State != daemonstate.SessionStateStopped || session.ObservedState != daemonstate.SessionStateStopped || session.TmuxAttachedCount != 0 {
		t.Fatalf("legacy session projection = %+v, want stopped repair marker", session)
	}
}

func TestTaskCloseBlocksLiveLegacyProjectRuntimeProjection(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-current"
	legacyProjectID := "proj-close-legacy-live"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Close with live legacy projection alias",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	worktreePath := filepath.Join(t.TempDir(), "legacy-live-"+taskID)
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir legacy worktree: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: legacyProjectID,
		IssueID:   taskID,
		Path:      worktreePath,
		Branch:    "riordan/" + taskID + "/legacy-live",
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed live legacy worktree projection: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger, BaseBranch: "main"},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}

	body, err := json.Marshal(taskCloseRequest{TaskID: taskID})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-live-legacy-projection",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "active runtime projection aliases remain") {
		t.Fatalf("task.close response = %+v, want live alias blocker", resp)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get issue after blocked close: %v", err)
	}
	if task.Status != domain.StatusInReview {
		t.Fatalf("task status = %s, want %s", task.Status, domain.StatusInReview)
	}
	if _, found, err := runtimeStore.GetWorktreeStateByIssueID(ctx, legacyProjectID, taskID); err != nil {
		t.Fatalf("get live legacy worktree projection: %v", err)
	} else if !found {
		t.Fatalf("live legacy worktree projection was removed for %s", taskID)
	}
}

func TestTaskCloseBlocksUnverifiedLegacyProjectSessionProjection(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-current"
	legacyProjectID := "proj-close-legacy-session"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Close with unverified legacy session alias",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := runtimeStore.UpsertSessionState(ctx, legacyProjectID, daemonstate.Session{
		ID:        "legacy-" + taskID,
		IssueID:   taskID,
		State:     daemonstate.SessionStateRunning,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed legacy session projection: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger, BaseBranch: "main"},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}

	body, err := json.Marshal(taskCloseRequest{TaskID: taskID})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-unverified-legacy-session",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "active runtime projection aliases remain") {
		t.Fatalf("task.close response = %+v, want unverified session alias blocker", resp)
	}
	session, found, err := runtimeStore.GetSessionState(ctx, legacyProjectID, "legacy-"+taskID)
	if err != nil {
		t.Fatalf("get legacy session projection: %v", err)
	}
	if !found || session.State != daemonstate.SessionStateRunning {
		t.Fatalf("legacy session projection = %+v found=%t, want running row preserved", session, found)
	}
}

func TestTaskCloseRunsIssueResourceCleanupWithoutSessionBeforeWorktreeRemoval(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-task-close-resource-cleanup"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Close with resource cleanup",
		Type:   domain.TypeTask,
		Status: domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	worktreePath := filepath.Join(repoDir, "wt-"+taskID)
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	branchName := "riordan/" + taskID + "/resource-cleanup"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      worktreePath,
		Branch:    branchName,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	cleanupMarker := filepath.Join(repoDir, "cleanup-marker")
	commands := make([]string, 0, 8)
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		joined := strings.Join(args, " ")
		commands = append(commands, joined)
		switch {
		case joined == "worktree list --porcelain":
			return fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", worktreePath, branchName), nil
		case strings.Contains(joined, "status --porcelain"):
			return "", nil
		case strings.HasPrefix(joined, "worktree remove "):
			if _, err := os.Stat(cleanupMarker); err != nil {
				return "", fmt.Errorf("cleanup marker missing before remove: %w", err)
			}
			return "", nil
		default:
			return "", nil
		}
	}}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			SessionShell: "sh",
			BaseBranch:   "main",
			IssueResources: appconfig.IssueResourcesConfig{
				CleanupCommands: []string{
					fmt.Sprintf("printf '%%s|%%s|%%s' \"$AZEDARACH_ISSUE_ID\" \"$AZEDARACH_WORKTREE_PATH\" \"$AZEDARACH_BRANCH\" > %q", cleanupMarker),
				},
			},
			Logger: logger,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		gitStatusAdapter: &gitServiceAdapter{
			client:            git.NewClient(runner, logger),
			runtimeStateStore: runtimeStore,
			logger:            logger,
			baseBranch:        "main",
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}

	body, err := json.Marshal(taskCloseRequest{TaskID: taskID})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-resource-cleanup",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.close",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if !resp.OK {
		if resp.Error != nil {
			t.Fatalf("handleTaskClose error = %s", resp.Error.Message)
		}
		t.Fatalf("handleTaskClose response = %+v", resp)
	}
	data, err := os.ReadFile(cleanupMarker)
	if err != nil {
		t.Fatalf("read cleanup marker: %v", err)
	}
	want := taskID + "|" + worktreePath + "|" + branchName
	if strings.TrimSpace(string(data)) != want {
		t.Fatalf("cleanup marker = %q, want %q", strings.TrimSpace(string(data)), want)
	}
	closed, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get task after close: %v", err)
	}
	if closed.Status != domain.StatusDone {
		t.Fatalf("task status = %s, want %s", closed.Status, domain.StatusDone)
	}
	if !strings.Contains(strings.Join(commands, "\n"), "worktree remove "+worktreePath) {
		t.Fatalf("commands = %v, want worktree remove", commands)
	}
}

func TestTaskCloseRunsIssueResourceCleanupWithSessionBeforeWorktreeRemoval(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-task-close-session-resource-cleanup"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Close with session resource cleanup",
		Type:   domain.TypeTask,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	worktreePath := filepath.Join(repoDir, "wt-"+taskID)
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	branchName := "riordan/" + taskID + "/session-resource-cleanup"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      worktreePath,
		Branch:    branchName,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}
	sessionID := naming.CanonicalSessionID(projectID, taskID)
	if err := runtimeStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID:             sessionID,
		IssueID:        taskID,
		State:          daemonstate.SessionStateAttached,
		ObservedState:  daemonstate.SessionStateAttached,
		Activity:       "idle",
		ActivitySource: "hooks",
		UpdatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed session projection: %v", err)
	}

	cleanupMarker := filepath.Join(repoDir, "cleanup-marker")
	commands := make([]string, 0, 8)
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		joined := strings.Join(args, " ")
		commands = append(commands, joined)
		switch {
		case joined == "worktree list --porcelain":
			return fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", worktreePath, branchName), nil
		case strings.Contains(joined, "status --porcelain"):
			return "", nil
		case strings.HasPrefix(joined, "worktree remove "):
			if _, err := os.Stat(cleanupMarker); err != nil {
				return "", fmt.Errorf("cleanup marker missing before remove: %w", err)
			}
			return "", nil
		default:
			return "", nil
		}
	}}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	tmuxRunner := newTestTmuxRunner(sessionID)
	close(tmuxRunner.killRelease)
	sessionStore := daemonstate.NewStore()
	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			SessionShell: "sh",
			BaseBranch:   "main",
			IssueResources: appconfig.IssueResourcesConfig{
				Env: map[string]string{
					"LOCAL_POSTGRES_DEV_DATABASE": "chefy_$AZEDARACH_ISSUE_ID",
				},
				CleanupCommands: []string{
					fmt.Sprintf("printf '%%s|%%s|%%s|%%s' \"$AZEDARACH_ISSUE_ID\" \"$AZEDARACH_WORKTREE_PATH\" \"$AZEDARACH_BRANCH\" \"$LOCAL_POSTGRES_DEV_DATABASE\" >> %q", cleanupMarker),
				},
			},
			Logger: logger,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		sessionStore: sessionStore,
		session:      daemonhandlers.NewSessionHandler(sessionStore),
		tmux:         tmux.NewClient(tmuxRunner, logger),
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		gitStatusAdapter: &gitServiceAdapter{
			client:            git.NewClient(runner, logger),
			runtimeStateStore: runtimeStore,
			logger:            logger,
			baseBranch:        "main",
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}

	body, err := json.Marshal(taskCloseRequest{TaskID: taskID})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-session-resource-cleanup",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.close",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if !resp.OK {
		if resp.Error != nil {
			t.Fatalf("handleTaskClose error = %s", resp.Error.Message)
		}
		t.Fatalf("handleTaskClose response = %+v", resp)
	}
	data, err := os.ReadFile(cleanupMarker)
	if err != nil {
		t.Fatalf("read cleanup marker: %v", err)
	}
	want := taskID + "|" + worktreePath + "|" + branchName + "|chefy_" + taskID
	if strings.TrimSpace(string(data)) != want {
		t.Fatalf("cleanup marker = %q, want %q", strings.TrimSpace(string(data)), want)
	}
	if !strings.Contains(strings.Join(commands, "\n"), "worktree remove "+worktreePath) {
		t.Fatalf("commands = %v, want worktree remove", commands)
	}
}

func TestCleanupTaskIssueResourcesRunsReconcileAbsentBeforeCleanup(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	worktreePath := filepath.Join(repoDir, "wt-az-1")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	marker := filepath.Join(repoDir, "resource-order")
	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			SessionShell: "sh",
			IssueResources: appconfig.IssueResourcesConfig{
				ReconcileCommand: fmt.Sprintf("printf '%%s\\n' \"$AZEDARACH_RESOURCE_DESIRED_STATE\" >> %q", marker),
				CleanupCommands: []string{
					fmt.Sprintf("printf 'cleanup\\n' >> %q", marker),
				},
			},
			Logger: slog.Default(),
		},
	}

	if err := d.cleanupTaskIssueResourcesForClose(ctx, protocol.DefaultProjectID, "az-1", worktreePath); err != nil {
		t.Fatalf("cleanupTaskIssueResourcesForClose error: %v", err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if got, want := strings.TrimSpace(string(data)), "absent\ncleanup"; got != want {
		t.Fatalf("resource order = %q, want %q", got, want)
	}
}

func TestCleanupTaskIssueResourcesReconcileAbsentFailureSkipsCleanup(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	worktreePath := filepath.Join(repoDir, "wt-az-1")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	marker := filepath.Join(repoDir, "resource-order")
	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			SessionShell: "sh",
			IssueResources: appconfig.IssueResourcesConfig{
				ReconcileCommand: "printf 'absent failed'; exit 7",
				CleanupCommands: []string{
					fmt.Sprintf("printf 'cleanup\\n' >> %q", marker),
				},
			},
			Logger: slog.Default(),
		},
	}

	err := d.cleanupTaskIssueResourcesForClose(ctx, protocol.DefaultProjectID, "az-1", worktreePath)
	if err == nil {
		t.Fatal("cleanupTaskIssueResourcesForClose error = nil, want reconcile failure")
	}
	if !strings.Contains(err.Error(), "absent failed") {
		t.Fatalf("cleanup error = %q, want reconcile command output", err.Error())
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("cleanup marker stat = %v, want not exist", statErr)
	}
}

func TestTaskUpdateStatusRejectsRawCloseActiveRuntime(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-guard-reopen-target"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Closed with live session",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := runtimeStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID:            "sess-" + taskID,
		IssueID:       taskID,
		State:         daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateAttached,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed session projection: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	body, err := json.Marshal(map[string]any{
		"task_id": taskID,
		"status":  domain.StatusDone,
	})
	if err != nil {
		t.Fatalf("marshal task update request: %v", err)
	}
	resp, err := d.handleTaskUpdateStatus(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-guard-reopen-target",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.update_status",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskUpdateStatus error: %v", err)
	}
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "status closed must be applied with task.close") {
		t.Fatalf("task.update_status response = %+v, want raw close rejection", resp)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get guarded issue after failed close: %v", err)
	}
	if task.Status != domain.StatusInProgress {
		t.Fatalf("guarded issue status = %s, want %s", task.Status, domain.StatusInProgress)
	}
	if strings.Contains(resp.Error.Message, "Moved closed blockers back for cleanup") {
		t.Fatalf("task.update_status response = %q, did not expect status repair for reachable active issue", resp.Error.Message)
	}
}

func TestTaskUpdateStatusRejectsRawCloseUnresolvedChildrenAndApplyPath(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-guard-children"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Parent",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if _, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Child",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInProgress,
		ParentID: &parentID,
	}); err != nil {
		t.Fatalf("create child: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}

	body, err := json.Marshal(map[string]any{
		"task_id": parentID,
		"status":  domain.StatusDone,
	})
	if err != nil {
		t.Fatalf("marshal task update request: %v", err)
	}
	resp, err := d.handleTaskUpdateStatus(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-guard-children",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.update_status",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskUpdateStatus error: %v", err)
	}
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "status closed must be applied with task.close") {
		t.Fatalf("task.update_status response = %+v, want raw close rejection", resp)
	}

	body, err = json.Marshal(map[string]any{
		"task_id": parentID,
		"status":  domain.StatusDone,
	})
	if err != nil {
		t.Fatalf("marshal skip task update request: %v", err)
	}
	resp, err = d.handleTaskUpdateStatus(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-guard-children-skip",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.update_status",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskUpdateStatus skip error: %v", err)
	}
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "status closed must be applied with task.close") {
		t.Fatalf("task.update_status skip response = %+v, want raw close rejection", resp)
	}

	_, err = d.Update(withDaemonProjectIDContext(ctx, projectID), parentID, domain.StatusDone)
	if err == nil || !strings.Contains(err.Error(), "status closed must be applied with task.close") {
		t.Fatalf("apply Update error = %v, want raw close rejection", err)
	}
}

func TestTaskCloseCommandIntegratesThroughDaemon(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-integrate"
	repoDir := t.TempDir()
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Integrate me",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	sourceWorktree := filepath.Join(repoDir, "wt-"+taskID)
	sourceBranch := "riordan/" + taskID + "/integrate"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      sourceWorktree,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	worktreeListOutput := fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", sourceWorktree, sourceBranch)
	commands := make([]string, 0, 12)
	var scratchWorktree string
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list":
			return worktreeListOutput, nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "status":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--git-common-dir":
			return filepath.Join(repoDir, ".git"), nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "HEAD":
			return "target-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "HEAD":
			return "merged-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "merge-base":
			return "base-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "diff" && slices.Contains(args, "--name-status"):
			return "M\tmain.go\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "diff":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-list" && args[4] == "main.."+sourceBranch:
			return "1", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "rev-list":
			return "0", nil
		case len(args) >= 6 && args[0] == "-C" && args[2] == "merge-tree":
			return "tree-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "fetch":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "checkout":
			return "", nil
		case len(args) >= 7 && args[0] == "-C" && args[1] == repoDir && args[2] == "worktree" && args[3] == "add":
			scratchWorktree = args[5]
			scratchHook := filepath.Join(scratchWorktree, ".git", "hooks", "commit-msg")
			if err := os.MkdirAll(filepath.Dir(scratchHook), 0o755); err != nil {
				t.Fatalf("mkdir scratch hooks: %v", err)
			}
			if err := os.WriteFile(scratchHook, []byte("#!/bin/sh\nsleep 1\n"), 0o755); err != nil {
				t.Fatalf("write scratch hook: %v", err)
			}
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "merge":
			time.Sleep(20 * time.Millisecond)
			return "merge complete", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "reset" && args[3] == "--hard":
			return "", nil
		case len(args) >= 6 && args[0] == "-C" && args[1] == repoDir && args[2] == "worktree" && args[3] == "remove":
			return "", nil
		default:
			return "", nil
		}
	}}

	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	body, err := json.Marshal(taskCloseRequest{
		TaskID:               taskID,
		IntegrateBeforeClose: true,
	})
	if err != nil {
		t.Fatalf("marshal task close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-integrate",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("handleTaskClose response = %+v", resp)
	}
	var result taskCloseResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal close result: %v", err)
	}
	if !result.IntegrationRequested || !result.Integrated || result.IntegratedSourceBranch != sourceBranch || result.IntegratedTargetBranch != "main" {
		t.Fatalf("close integration result = %+v", result)
	}
	hookPhase, ok := taskClosePhaseByName(result.Phases, "githook.commit-msg")
	if !ok {
		t.Fatalf("close phases = %+v, want githook.commit-msg", result.Phases)
	}
	if hookPhase.Hook != "commit-msg" || hookPhase.Command != "git merge --no-edit "+sourceBranch || hookPhase.ElapsedMS < 15 {
		t.Fatalf("hook phase = %+v, want commit-msg merge timing", hookPhase)
	}
	if hookPhase.ExitStatus == nil || *hookPhase.ExitStatus != 0 || hookPhase.Blocking == nil || !*hookPhase.Blocking {
		t.Fatalf("hook phase = %+v, want exit_status=0 blocking=true", hookPhase)
	}
	closed, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get task after close: %v", err)
	}
	if closed.Status != domain.StatusDone {
		t.Fatalf("task status = %s, want %s", closed.Status, domain.StatusDone)
	}
	joined := strings.Join(commands, "\n")
	for _, want := range []string{
		"-C " + sourceWorktree + " status --porcelain",
		"-C " + repoDir + " merge-tree --write-tree main " + sourceBranch,
		"-C " + repoDir + " checkout main",
		"-C " + repoDir + " worktree add --detach ",
		"merge --no-edit " + sourceBranch,
		"-C " + repoDir + " reset --hard merged-sha",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("git commands missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "-C "+repoDir+" fetch origin") {
		t.Fatalf("git commands should not fetch during local close integration:\n%s", joined)
	}
}

func TestTaskCloseCommandIntegrationIgnoresDuplicateIssueTargetWorktreeFromOtherProject(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-target"
	repoDir := t.TempDir()
	otherRepo := filepath.Join(t.TempDir(), "Chefy-production")
	if err := os.MkdirAll(otherRepo, 0o755); err != nil {
		t.Fatalf("mkdir other repo: %v", err)
	}
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Duplicate issue id integration",
		Type:     domain.TypeBug,
		Priority: domain.P0,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	sourceWorktree := filepath.Join(repoDir, "wt-"+taskID)
	sourceBranch := "riordan/" + taskID + "/integrate"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      sourceWorktree,
		Branch:    sourceBranch,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	worktreeListOutput := strings.Join([]string{
		fmt.Sprintf("worktree %s\nbranch refs/heads/main\n", otherRepo),
		fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n", sourceWorktree, sourceBranch),
	}, "\n")
	commands := make([]string, 0, 24)
	var scratchWorktree string
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		joined := strings.Join(args, " ")
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list":
			return worktreeListOutput, nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == otherRepo && args[2] == "status":
			return "A  domain/commerce/tsconfig.json\n", nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "status":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--git-common-dir":
			return filepath.Join(repoDir, ".git"), nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "HEAD":
			return "target-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "HEAD":
			return "merged-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "merge-base":
			return "base-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "diff" && slices.Contains(args, "--name-status"):
			return "M\tmain.go\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "diff":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-list" && args[4] == "main.."+sourceBranch:
			return "1", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "rev-list":
			return "0", nil
		case len(args) >= 6 && args[0] == "-C" && args[2] == "merge-tree":
			return "tree-sha", nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "checkout":
			return "", nil
		case len(args) >= 7 && args[0] == "-C" && args[1] == repoDir && args[2] == "worktree" && args[3] == "add":
			scratchWorktree = args[5]
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "merge":
			return "merge complete", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "reset" && args[3] == "--hard":
			return "", nil
		case len(args) >= 6 && args[0] == "-C" && args[1] == repoDir && args[2] == "worktree" && args[3] == "remove":
			return "", nil
		default:
			return "", fmt.Errorf("unexpected git args: %s", joined)
		}
	}}

	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	body, err := json.Marshal(taskCloseRequest{
		TaskID:               taskID,
		IntegrateBeforeClose: true,
	})
	if err != nil {
		t.Fatalf("marshal task close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-duplicate-project",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if !resp.OK {
		if resp.Error != nil {
			t.Fatalf("handleTaskClose error = %s", resp.Error.Message)
		}
		t.Fatalf("handleTaskClose response = %+v", resp)
	}
	var result taskCloseResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal close result: %v", err)
	}
	if !result.IntegrationRequested || !result.Integrated || result.IntegratedTargetBranch != "main" {
		t.Fatalf("close integration result = %+v, want project-scoped integration to main", result)
	}
	joined := strings.Join(commands, "\n")
	if strings.Contains(joined, "-C "+otherRepo+" ") {
		t.Fatalf("git commands used non-target project worktree:\n%s", joined)
	}
	if !strings.Contains(joined, "-C "+repoDir+" status --porcelain") {
		t.Fatalf("git commands missing requested project target status:\n%s", joined)
	}
}

func TestTaskCloseCommandReportsIntegratedCleanupFailureAndRetrySkipsMerge(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-integrate-cleanup-fail-retry"
	repoDir := t.TempDir()
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Integrated cleanup retry",
		Type:     domain.TypeBug,
		Priority: domain.P1,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	sourceWorktree := filepath.Join(repoDir, "wt-"+taskID)
	if err := os.MkdirAll(sourceWorktree, 0o755); err != nil {
		t.Fatalf("mkdir source worktree: %v", err)
	}
	sourceBranch := "riordan/" + taskID + "/integrated-cleanup-retry"
	projectionDB, err := sql.Open("sqlite", issuesDBPath)
	if err != nil {
		t.Fatalf("open projection db: %v", err)
	}
	defer projectionDB.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := projectionDB.ExecContext(ctx, `
		INSERT INTO daemon_worktree_projections (project_id, issue_id, path, branch, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`, projectID, taskID, sourceWorktree, sourceBranch, now); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}
	preflightTasks, err := issuesClient.ListParentChildSubtreeWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("precondition list runtime subtree: %v", err)
	}
	if len(preflightTasks) != 1 || !preflightTasks[0].HasWorktree {
		t.Fatalf("precondition task runtime = %+v, want worktree projection", preflightTasks)
	}

	commands := make([]string, 0, 32)
	var scratchWorktree string
	sourceUniqueReads := 0
	removeAttempts := 0
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		joined := strings.Join(args, " ")
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list":
			if removeAttempts >= 2 {
				return fmt.Sprintf("worktree %s\nbranch refs/heads/main\n\n", repoDir), nil
			}
			return fmt.Sprintf("worktree %s\nbranch refs/heads/main\n\nworktree %s\nbranch refs/heads/%s\n\n", repoDir, sourceWorktree, sourceBranch), nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "status":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--git-common-dir":
			return filepath.Join(repoDir, ".git"), nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "HEAD":
			return "target-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "HEAD":
			return "merged-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "merge-base":
			return "base-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "diff" && slices.Contains(args, "--name-status"):
			return "M\tmain.go\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "diff":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-list" && args[3] == "--count" && args[4] == "main.."+sourceBranch:
			sourceUniqueReads++
			if sourceUniqueReads == 1 {
				return "1", nil
			}
			return "0", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "rev-list":
			return "0", nil
		case len(args) >= 6 && args[0] == "-C" && args[2] == "merge-tree":
			return "tree-sha", nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "fetch":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "checkout":
			return "", nil
		case len(args) >= 7 && args[0] == "-C" && args[1] == repoDir && args[2] == "worktree" && args[3] == "add":
			scratchWorktree = args[5]
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "merge":
			return "merge complete", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "reset" && args[3] == "--hard":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "worktree" && args[3] == "remove":
			removedPath := args[len(args)-1]
			if removedPath == scratchWorktree {
				return "", nil
			}
			if removedPath != sourceWorktree {
				return "", fmt.Errorf("unexpected worktree remove path: %s", removedPath)
			}
			removeAttempts++
			if removeAttempts == 1 {
				return "fatal: '" + sourceWorktree + "' contains modified or untracked files, use --force to delete it", fmt.Errorf("worktree remove blocked by local changes")
			}
			if err := os.RemoveAll(sourceWorktree); err != nil {
				return "", err
			}
			return "", nil
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "remove":
			removedPath := args[len(args)-1]
			if removedPath != sourceWorktree {
				return "", fmt.Errorf("unexpected worktree remove path: %s", removedPath)
			}
			removeAttempts++
			if removeAttempts == 1 {
				return "fatal: '" + sourceWorktree + "' contains modified or untracked files, use --force to delete it", fmt.Errorf("worktree remove blocked by local changes")
			}
			if err := os.RemoveAll(sourceWorktree); err != nil {
				return "", err
			}
			return "", nil
		case len(args) >= 3 && args[0] == "branch" && args[1] == "-D":
			return "", nil
		default:
			return "", fmt.Errorf("unexpected git args: %s", joined)
		}
	}}

	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.gitStatusAdapter = &gitServiceAdapter{
		client:            git.NewClient(runner, logger),
		runtimeStateStore: runtimeStore,
		logger:            logger,
		baseBranch:        "main",
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	body, err := json.Marshal(taskCloseRequest{
		TaskID:               taskID,
		IntegrateBeforeClose: true,
	})
	if err != nil {
		t.Fatalf("marshal task close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-integrated-cleanup-fail",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose first error: %v", err)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("first handleTaskClose response = %+v, want cleanup failure", resp)
	}
	for _, want := range []string{"Integration already completed", sourceBranch, "landed on main", "cleanup/status remains", "Next:"} {
		if !strings.Contains(resp.Error.Message, want) {
			t.Fatalf("first close error = %q, missing %q", resp.Error.Message, want)
		}
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get task after failed close: %v", err)
	}
	if task.Status != domain.StatusInReview {
		t.Fatalf("task status after failed close = %s, want %s", task.Status, domain.StatusInReview)
	}

	resp, err = d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-integrated-cleanup-retry",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose retry error: %v", err)
	}
	if !resp.OK {
		if resp.Error != nil {
			t.Fatalf("retry handleTaskClose error = %s\ncommands:\n%s", resp.Error.Message, strings.Join(commands, "\n"))
		}
		t.Fatalf("retry handleTaskClose response = %+v", resp)
	}
	var result taskCloseResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal retry close result: %v", err)
	}
	if !result.IntegrationRequested || result.Integrated || !result.WorktreeRemoved {
		t.Fatalf("retry close result = %+v, want no-op integration plus worktree cleanup", result)
	}
	closed, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get task after retry close: %v", err)
	}
	if closed.Status != domain.StatusDone {
		t.Fatalf("task status after retry = %s, want %s", closed.Status, domain.StatusDone)
	}
	joined := strings.Join(commands, "\n")
	if got := strings.Count(joined, "merge --no-edit "+sourceBranch); got != 1 {
		t.Fatalf("merge count = %d, want one initial merge only:\n%s", got, joined)
	}
	if sourceUniqueReads != 2 {
		t.Fatalf("main..source containment reads = %d, want first merge check plus retry no-op check", sourceUniqueReads)
	}
	if removeAttempts != 2 {
		t.Fatalf("worktree remove attempts = %d, want failed first cleanup and successful retry", removeAttempts)
	}
}

func TestTaskClosePostIntegrationPhaseErrorTreatsNoChangesAsIntegratedEvidence(t *testing.T) {
	err := taskClosePostIntegrationPhaseError("az-1", "status_write", taskCloseIntegrationResult{
		Requested:    true,
		NoChanges:    true,
		SourceBranch: "riordan/az-1/already-landed",
		TargetBranch: "main",
	}, fmt.Errorf("status write failed"))

	for _, want := range []string{"Integration already completed", "riordan/az-1/already-landed", "landed on main", "retry close"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, missing %q", err.Error(), want)
		}
	}
}

func TestTaskCloseIntegrationRetriesRepeatedlyWhenTargetHeadMovesAfterScratchValidation(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-integrate-retry-stale-target"
	repoDir := t.TempDir()
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Retry stale target",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	sourceWorktree := filepath.Join(repoDir, "wt-"+taskID)
	sourceBranch := "riordan/" + taskID + "/retry-stale-target"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      sourceWorktree,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	worktreeListOutput := fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", sourceWorktree, sourceBranch)
	commands := make([]string, 0, 24)
	var scratchWorktree string
	scratchDesiredHeads := map[string]string{}
	targetHeadReads := 0
	scratchAdds := 0
	resets := make([]string, 0, 1)
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list":
			return worktreeListOutput, nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "status":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--git-common-dir":
			return filepath.Join(repoDir, ".git"), nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "HEAD":
			targetHeadReads++
			switch targetHeadReads {
			case 1:
				return "target-sha-1", nil
			case 2, 3:
				return "target-sha-2", nil
			default:
				return "target-sha-3", nil
			}
		case len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "HEAD":
			return scratchDesiredHeads[scratchWorktree], nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "merge-base":
			return "base-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "diff" && slices.Contains(args, "--name-status"):
			return "M\tmain.go\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "diff":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-list" && args[4] == "main.."+sourceBranch:
			return "1", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "rev-list":
			return "0", nil
		case len(args) >= 6 && args[0] == "-C" && args[2] == "merge-tree":
			return "tree-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "fetch":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "checkout":
			return "", nil
		case len(args) >= 7 && args[0] == "-C" && args[1] == repoDir && args[2] == "worktree" && args[3] == "add":
			scratchAdds++
			scratchWorktree = args[5]
			scratchDesiredHeads[scratchWorktree] = fmt.Sprintf("merged-sha-%d", scratchAdds)
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "merge":
			return "merge complete", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "reset" && args[3] == "--hard":
			resets = append(resets, args[4])
			return "", nil
		case len(args) >= 6 && args[0] == "-C" && args[1] == repoDir && args[2] == "worktree" && args[3] == "remove":
			return "", nil
		default:
			return "", nil
		}
	}}

	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	result, err := d.integrateTaskBeforeClose(ctx, projectID, taskID, true)
	if err != nil {
		t.Fatalf("integrateTaskBeforeClose error: %v", err)
	}
	if !result.Integrated {
		t.Fatalf("integration result = %+v, want integrated after retry", result)
	}
	if scratchAdds != 3 {
		t.Fatalf("scratch merge attempts = %d, want 3", scratchAdds)
	}
	if !slices.Equal(resets, []string{"merged-sha-3"}) {
		t.Fatalf("reset refs = %v, want retry final apply only", resets)
	}
	joined := strings.Join(commands, "\n")
	if !strings.Contains(joined, "worktree add --detach ") || !strings.Contains(joined, " target-sha-3") {
		t.Fatalf("git commands missing retry scratch from moved target:\n%s", joined)
	}
}

func TestTaskCloseIntegrationBaseFallbackUsesProjectRepo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	logger := slog.Default()
	bootstrapRepo := t.TempDir()
	projectRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bootstrapRepo, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir bootstrap .azedarach: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectRepo, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir project .azedarach: %v", err)
	}
	if err := appconfig.SaveProjectsRegistry(&appconfig.ProjectsRegistry{
		Projects: []appconfig.Project{{Name: "other", Path: projectRepo}},
	}); err != nil {
		t.Fatalf("save projects registry: %v", err)
	}
	projectID, err := appconfig.ProjectIDForRoot(projectRepo)
	if err != nil {
		t.Fatalf("ProjectIDForRoot(project): %v", err)
	}

	issuesClient := issues.NewClient(projectRepo, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Integrate me",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	sourceWorktree := filepath.Join(projectRepo, "wt-"+taskID)
	sourceBranch := "riordan/" + taskID + "/integrate"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      sourceWorktree,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	worktreeListOutput := fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", sourceWorktree, sourceBranch)
	commands := make([]string, 0, 12)
	var scratchWorktree string
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list":
			return worktreeListOutput, nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "status":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == projectRepo && args[2] == "rev-parse" && args[3] == "--git-common-dir":
			return filepath.Join(projectRepo, ".git"), nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == projectRepo && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "HEAD":
			return "target-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "HEAD":
			return "merged-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "merge-base":
			return "base-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "diff" && slices.Contains(args, "--name-status"):
			return "M\tmain.go\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "diff":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == projectRepo && args[2] == "rev-list" && args[4] == "main.."+sourceBranch:
			return "1", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "rev-list":
			return "0", nil
		case len(args) >= 6 && args[0] == "-C" && args[1] == projectRepo && args[2] == "merge-tree":
			return "tree-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "fetch":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "checkout":
			return "", nil
		case len(args) >= 7 && args[0] == "-C" && args[1] == projectRepo && args[2] == "worktree" && args[3] == "add":
			scratchWorktree = args[5]
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "merge":
			return "merge complete", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == projectRepo && args[2] == "reset" && args[3] == "--hard":
			return "", nil
		case len(args) >= 6 && args[0] == "-C" && args[1] == projectRepo && args[2] == "worktree" && args[3] == "remove":
			return "", nil
		default:
			return "", nil
		}
	}}

	manager := git.NewWorktreeManager(runner, projectRepo, logger)
	d := &Daemon{
		cfg: Config{RepoDir: bootstrapRepo, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			protocol.NormalizeProjectID(projectID): issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			protocol.NormalizeProjectID(projectID): runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			protocol.NormalizeProjectID(projectID): manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{protocol.NormalizeProjectID(projectID): 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	result, err := d.integrateTaskBeforeClose(ctx, projectID, taskID, true)
	if err != nil {
		t.Fatalf("integrateTaskBeforeClose error: %v", err)
	}
	if !result.Integrated {
		t.Fatalf("integration result = %+v, want integrated", result)
	}
	joined := strings.Join(commands, "\n")
	if strings.Contains(joined, "-C "+bootstrapRepo+" ") {
		t.Fatalf("git commands used bootstrap repo, want project repo:\n%s", joined)
	}
	if !strings.Contains(joined, "-C "+projectRepo+" worktree add --detach ") {
		t.Fatalf("git commands missing project repo worktree add:\n%s", joined)
	}
}

func TestTaskCloseCommandKeepsTargetCleanWhenScratchMergeDirties(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-integrate-post-merge-dirty"
	repoDir := t.TempDir()
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Dirty post-merge target",
		Type:     domain.TypeBug,
		Priority: domain.P1,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	sourceWorktree := filepath.Join(repoDir, "wt-"+taskID)
	sourceBranch := "riordan/" + taskID + "/dirty-post-merge"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      sourceWorktree,
		Branch:    sourceBranch,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	worktreeListOutput := fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", sourceWorktree, sourceBranch)
	commands := make([]string, 0, 16)
	var scratchWorktree string
	scratchStatusReads := 0
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list":
			return worktreeListOutput, nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "status":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "status":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "status":
			scratchStatusReads++
			if scratchStatusReads >= 2 {
				return "A  hook-created.txt\n", nil
			}
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--git-common-dir":
			return filepath.Join(repoDir, ".git"), nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "HEAD":
			return "target-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "rev-parse" && args[3] == "-q" && args[4] == "--verify":
			return "", fmt.Errorf("no merge head")
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "merge-base":
			return "base-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "diff" && slices.Contains(args, "--name-status"):
			return "M\tmain.go\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "diff":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-list" && args[4] == "main.."+sourceBranch:
			return "1", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "rev-list":
			return "0", nil
		case len(args) >= 6 && args[0] == "-C" && args[1] == repoDir && args[2] == "merge-tree":
			return "tree-sha", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "fetch":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "checkout":
			return "", nil
		case len(args) >= 7 && args[0] == "-C" && args[1] == repoDir && args[2] == "worktree" && args[3] == "add":
			scratchWorktree = args[5]
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "merge":
			return "Merge made by the 'ort' strategy.", nil
		case len(args) >= 6 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "restore":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == scratchWorktree && args[2] == "clean":
			return "", nil
		case len(args) >= 6 && args[0] == "-C" && args[1] == repoDir && args[2] == "worktree" && args[3] == "remove":
			return "", nil
		default:
			t.Fatalf("unexpected git args: %v", args)
			return "", nil
		}
	}}

	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		logger: logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	body, err := json.Marshal(taskCloseRequest{
		TaskID:               taskID,
		IntegrateBeforeClose: true,
	})
	if err != nil {
		t.Fatalf("marshal task close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-integrate-post-merge-dirty",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("handleTaskClose response = %+v, want integration failure", resp)
	}
	if !strings.Contains(resp.Error.Message, "post-merge hooks") || !strings.Contains(resp.Error.Message, "hook-created.txt") {
		t.Fatalf("handleTaskClose error = %q, want post-merge dirty detail", resp.Error.Message)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get task after failed close: %v", err)
	}
	if task.Status != domain.StatusInReview {
		t.Fatalf("task status = %s, want %s", task.Status, domain.StatusInReview)
	}
	joined := strings.Join(commands, "\n")
	for _, want := range []string{
		"-C " + repoDir + " worktree add --detach ",
		"merge --no-edit " + sourceBranch,
		"restore --staged --worktree .",
		"clean -fd",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("git commands missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "-C "+repoDir+" reset --hard") {
		t.Fatalf("target should not be reset after scratch merge failure:\n%s", joined)
	}
}

func TestTaskCloseCommandSkipsIntegrationWhenSourceHasNoChangesEvenIfTargetDirty(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-integrate-no-changes"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Close no-op branch",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	sourceWorktree := filepath.Join(repoDir, "wt-"+taskID)
	if err := os.MkdirAll(sourceWorktree, 0o755); err != nil {
		t.Fatalf("mkdir source worktree: %v", err)
	}
	sourceBranch := "riordan/" + taskID + "/no-changes"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      sourceWorktree,
		Branch:    sourceBranch,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	worktreeListOutput := fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", sourceWorktree, sourceBranch)
	commands := make([]string, 0, 12)
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		joined := strings.Join(args, " ")
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list":
			return worktreeListOutput, nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "status":
			return " M README.md\n", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "status":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "merge-base":
			return "base-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "diff" && slices.Contains(args, "--name-status"):
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "diff":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "rev-list" && args[4] == "HEAD..main":
			return "0", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "rev-list" && args[4] == "main..HEAD":
			return "2", nil
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "remove":
			if err := os.RemoveAll(sourceWorktree); err != nil {
				return "", err
			}
			return "", nil
		case len(args) >= 3 && args[0] == "branch" && args[1] == "-D":
			return "", nil
		default:
			return "", fmt.Errorf("unexpected git args: %s", joined)
		}
	}}

	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.operationRuntime = newOperationRuntime(operationRuntimeConfig{
		repoDir:      repoDir,
		logger:       logger,
		hub:          d.hub,
		nextRevision: d.nextRevision,
	})
	d.gitStatusAdapter = &gitServiceAdapter{
		client:            git.NewClient(runner, logger),
		runtimeStateStore: runtimeStore,
		logger:            logger,
		baseBranch:        "main",
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		logger: logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	beforeCloseTasks, err := issuesClient.ListWithRuntime(ctx, projectID)
	if err != nil {
		t.Fatalf("list tasks before close: %v", err)
	}
	var beforeClose domain.Task
	for _, task := range beforeCloseTasks {
		if task.ID.String() == taskID {
			beforeClose = task
			break
		}
	}
	if beforeClose.ID.IsZero() {
		t.Fatalf("task %s missing before close", taskID)
	}
	if !beforeClose.HasWorktree {
		t.Fatalf("task before close HasWorktree = false, want seeded runtime projection; task = %+v", beforeClose)
	}

	body, err := json.Marshal(taskCloseRequest{
		TaskID:               taskID,
		IntegrateBeforeClose: true,
	})
	if err != nil {
		t.Fatalf("marshal task close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-integrate-no-changes",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if !resp.OK {
		if resp.Error != nil {
			t.Fatalf("handleTaskClose error = %s", resp.Error.Message)
		}
		t.Fatalf("handleTaskClose response = %+v", resp)
	}
	var result taskCloseResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal close result: %v", err)
	}
	if !result.IntegrationRequested || result.Integrated {
		t.Fatalf("close integration result = %+v, want requested no-op integration", result)
	}
	if !result.WorktreeRemoved {
		t.Fatalf("close integration result = %+v, want worktree cleanup", result)
	}
	closed, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get task after close: %v", err)
	}
	if closed.Status != domain.StatusDone {
		t.Fatalf("task status = %s, want %s", closed.Status, domain.StatusDone)
	}
	joined := strings.Join(commands, "\n")
	for _, blocked := range []string{
		"-C " + repoDir + " checkout main",
		"-C " + repoDir + " merge --no-edit " + sourceBranch,
		"-C " + repoDir + " merge-tree --write-tree main " + sourceBranch,
	} {
		if strings.Contains(joined, blocked) {
			t.Fatalf("git commands should skip no-op integration but included %q:\n%s", blocked, joined)
		}
	}
	for _, want := range []string{
		"-C " + sourceWorktree + " rev-list --count main..HEAD",
		"worktree remove " + sourceWorktree,
		"branch -D " + sourceBranch,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("git commands missing %q:\n%s", want, joined)
		}
	}
}

func TestTaskCloseCommandDirtyChildTargetNamesPathsAndRecovery(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-child-dirty-target"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Parent integration target",
		Type:     domain.TypeFeature,
		Priority: domain.P2,
		Status:   domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Child ready to close",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child task: %v", err)
	}

	parentWorktree := filepath.Join(repoDir, "wt-"+parentID)
	childWorktree := filepath.Join(repoDir, "wt-"+childID)
	parentBranch := "riordan/" + parentID + "/parent"
	childBranch := "riordan/" + childID + "/child"
	for _, state := range []daemonstate.WorktreeState{
		{
			ProjectID: projectID,
			IssueID:   parentID,
			Path:      parentWorktree,
			Branch:    parentBranch,
			UpdatedAt: time.Now().UTC(),
		},
		{
			ProjectID: projectID,
			IssueID:   childID,
			Path:      childWorktree,
			Branch:    childBranch,
			UpdatedAt: time.Now().UTC(),
		},
	} {
		if err := runtimeStore.UpsertWorktreeState(ctx, state); err != nil {
			t.Fatalf("seed worktree projection: %v", err)
		}
	}

	worktreeListOutput := strings.Join([]string{
		fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n", parentWorktree, parentBranch),
		fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n", childWorktree, childBranch),
	}, "\n")
	commands := make([]string, 0, 12)
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		joined := strings.Join(args, " ")
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list":
			return worktreeListOutput, nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == parentWorktree && args[2] == "rev-list" && args[4] == parentBranch+".."+childBranch:
			return "1", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == childWorktree && args[2] == "status":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == parentWorktree && args[2] == "status":
			return "A  staged-target.go\n M modified-target.go\n?? scratch-target.log\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == childWorktree && args[2] == "merge-base":
			return "base-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == childWorktree && args[2] == "diff" && slices.Contains(args, "--name-status"):
			return "M\tchild-change.go\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == childWorktree && args[2] == "diff":
			return "", nil
		default:
			return "", fmt.Errorf("unexpected git args: %s", joined)
		}
	}}

	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		logger: logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	body, err := json.Marshal(taskCloseRequest{
		TaskID:               childID,
		IntegrateBeforeClose: true,
	})
	if err != nil {
		t.Fatalf("marshal task close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-child-dirty-target",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("handleTaskClose response = %+v, want dirty target failure", resp)
	}
	message := resp.Error.Message
	for _, want := range []string{
		"target branch worktree is not clean",
		"1 staged, 1 modified, 1 untracked",
		"staged: staged-target.go",
		"modified: modified-target.go",
		"untracked: scratch-target.log",
		"target-side dirt, not source worker dirt",
		"child " + childID + " evidence may still be valid",
		"git -C " + fmt.Sprintf("%q", parentWorktree) + " status --short",
		"stash push -u -m \"az issue close target drift before " + childID + "\"",
		"commit it intentionally",
		"abort and leave the child open/in_review",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("handleTaskClose error = %q, missing %q", message, want)
		}
	}
	if strings.Contains(strings.Join(commands, "\n"), "merge --no-edit") {
		t.Fatalf("dirty target preflight should block before merge, commands:\n%s", strings.Join(commands, "\n"))
	}
}

func TestGitStatusSummaryWithDetailsBoundsPathOutput(t *testing.T) {
	modified := make([]string, 0, gitStatusDetailPathLimit+2)
	for i := 0; i < gitStatusDetailPathLimit+2; i++ {
		modified = append(modified, fmt.Sprintf("file-%02d.go", i))
	}

	got := gitStatusSummaryWithDetails(&git.GitStatus{
		HasChanges: true,
		Modified:   modified,
	})

	if !strings.Contains(got, fmt.Sprintf("%d modified", gitStatusDetailPathLimit+2)) {
		t.Fatalf("summary = %q, want total modified count", got)
	}
	if !strings.Contains(got, "file-11.go") {
		t.Fatalf("summary = %q, want last in-bounds path", got)
	}
	if strings.Contains(got, "file-12.go") || strings.Contains(got, "file-13.go") {
		t.Fatalf("summary = %q, want paths beyond bounded output omitted", got)
	}
	if !strings.Contains(got, "2 more omitted") {
		t.Fatalf("summary = %q, want omitted count", got)
	}
}

func TestTaskCloseCommandSkipsIntegrationWhenSourceAlreadyReachableFromTarget(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-integrate-already-reachable"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Close already integrated branch",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	sourceWorktree := filepath.Join(repoDir, "wt-"+taskID)
	if err := os.MkdirAll(sourceWorktree, 0o755); err != nil {
		t.Fatalf("mkdir source worktree: %v", err)
	}
	sourceBranch := "riordan/" + taskID + "/already-integrated"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      sourceWorktree,
		Branch:    sourceBranch,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	worktreeListOutput := fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", sourceWorktree, sourceBranch)
	commands := make([]string, 0, 12)
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		joined := strings.Join(args, " ")
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list":
			return worktreeListOutput, nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-list" && args[3] == "--count" && args[4] == "main.."+sourceBranch:
			return "0", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "status":
			return "", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == repoDir && args[2] == "status":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "merge-base":
			return "stale-base-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "diff" && slices.Contains(args, "--name-status"):
			return "M\talready-integrated.go\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "diff":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "rev-list" && args[4] == "HEAD..main":
			return "3", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "rev-list" && args[4] == "main..HEAD":
			return "2", nil
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "remove":
			if err := os.RemoveAll(sourceWorktree); err != nil {
				return "", err
			}
			return "", nil
		case len(args) >= 3 && args[0] == "branch" && args[1] == "-D":
			return "", nil
		default:
			return "", fmt.Errorf("unexpected git args: %s", joined)
		}
	}}

	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.operationRuntime = newOperationRuntime(operationRuntimeConfig{
		repoDir:      repoDir,
		logger:       logger,
		hub:          d.hub,
		nextRevision: d.nextRevision,
	})
	d.gitStatusAdapter = &gitServiceAdapter{
		client:            git.NewClient(runner, logger),
		runtimeStateStore: runtimeStore,
		logger:            logger,
		baseBranch:        "main",
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		logger: logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	body, err := json.Marshal(taskCloseRequest{
		TaskID:               taskID,
		IntegrateBeforeClose: true,
	})
	if err != nil {
		t.Fatalf("marshal task close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-integrate-already-reachable",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if !resp.OK {
		if resp.Error != nil {
			t.Fatalf("handleTaskClose error = %s", resp.Error.Message)
		}
		t.Fatalf("handleTaskClose response = %+v", resp)
	}
	var result taskCloseResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal close result: %v", err)
	}
	if !result.IntegrationRequested || result.Integrated {
		t.Fatalf("close integration result = %+v, want requested no-op integration", result)
	}
	if !result.WorktreeRemoved {
		t.Fatalf("close integration result = %+v, want worktree cleanup", result)
	}
	closed, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get task after close: %v", err)
	}
	if closed.Status != domain.StatusDone {
		t.Fatalf("task status = %s, want %s", closed.Status, domain.StatusDone)
	}
	joined := strings.Join(commands, "\n")
	for _, blocked := range []string{
		"-C " + repoDir + " merge-tree --write-tree main " + sourceBranch,
		"merge --no-edit " + sourceBranch,
		"-C " + sourceWorktree + " diff --name-status",
	} {
		if strings.Contains(joined, blocked) {
			t.Fatalf("git commands should skip already-integrated source but included %q:\n%s", blocked, joined)
		}
	}
	for _, want := range []string{
		"-C " + repoDir + " rev-list --count main.." + sourceBranch,
		"worktree remove " + sourceWorktree,
		"branch -D " + sourceBranch,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("git commands missing %q:\n%s", want, joined)
		}
	}
}

func TestTaskCloseCommandForceRemovesDirtyAlreadyIntegratedWorktree(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-force-dirty-already-reachable"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Force close dirty stale worktree",
		Type:     domain.TypeBug,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	sourceWorktree := filepath.Join(repoDir, "wt-"+taskID)
	if err := os.MkdirAll(sourceWorktree, 0o755); err != nil {
		t.Fatalf("mkdir source worktree: %v", err)
	}
	sourceBranch := "riordan/" + taskID + "/already-integrated-dirty"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      sourceWorktree,
		Branch:    sourceBranch,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	worktreeListOutput := fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", sourceWorktree, sourceBranch)
	commands := make([]string, 0, 12)
	removeStarted := make(chan struct{})
	releaseRemove := make(chan struct{})
	removeDone := make(chan struct{})
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		joined := strings.Join(args, " ")
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list":
			return worktreeListOutput, nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-list" && args[3] == "--count" && args[4] == "main.."+sourceBranch:
			return "0", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == sourceWorktree && args[2] == "status":
			return "?? generated.txt\n", fmt.Errorf("forced close should not inspect dirty source status")
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "remove" && slices.Contains(args, "--force") && len(args) >= 5:
			close(removeStarted)
			<-releaseRemove
			if err := os.RemoveAll(sourceWorktree); err != nil {
				return "", err
			}
			close(removeDone)
			return "", nil
		case len(args) >= 3 && args[0] == "branch" && args[1] == "-D":
			return "", nil
		default:
			return "", fmt.Errorf("unexpected git args: %s", joined)
		}
	}}

	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.operationRuntime = newOperationRuntime(operationRuntimeConfig{
		repoDir:      repoDir,
		logger:       logger,
		hub:          d.hub,
		nextRevision: d.nextRevision,
	})
	d.gitStatusAdapter = &gitServiceAdapter{
		client:            git.NewClient(runner, logger),
		runtimeStateStore: runtimeStore,
		logger:            logger,
		baseBranch:        "main",
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		logger: logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	body, err := json.Marshal(taskCloseRequest{
		TaskID:               taskID,
		ForceWorktree:        true,
		IntegrateBeforeClose: true,
	})
	if err != nil {
		t.Fatalf("marshal task close request: %v", err)
	}
	type closeResponse struct {
		resp protocol.ResponseEnvelope
		err  error
	}
	closeDone := make(chan closeResponse, 1)
	go func() {
		resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-close-force-dirty-already-reachable",
			Kind:            protocol.EnvelopeKindCommand,
			Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
			Command:         "task.close",
			Body:            body,
		})
		closeDone <- closeResponse{resp: resp, err: err}
	}()
	var closeResult closeResponse
	select {
	case closeResult = <-closeDone:
	case <-time.After(500 * time.Millisecond):
		close(releaseRemove)
		t.Fatal("handleTaskClose blocked on forced physical worktree removal")
	}
	resp := closeResult.resp
	if closeResult.err != nil {
		t.Fatalf("handleTaskClose error: %v", closeResult.err)
	}
	if !resp.OK {
		if resp.Error != nil {
			t.Fatalf("handleTaskClose error = %s", resp.Error.Message)
		}
		t.Fatalf("handleTaskClose response = %+v", resp)
	}
	var result taskCloseResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal close result: %v", err)
	}
	if !result.IntegrationRequested || result.Integrated || !result.WorktreeForced || !result.WorktreeCleanupDeferred || result.WorktreeRemoved || result.WorktreeCleanupOperationID == "" {
		t.Fatalf("close result = %+v, want requested no-op integration with forced worktree cleanup", result)
	}
	closed, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get task after close: %v", err)
	}
	if closed.Status != domain.StatusDone || closed.HasWorktree {
		t.Fatalf("closed task status=%s has_worktree=%v, want closed without worktree", closed.Status, closed.HasWorktree)
	}
	select {
	case <-removeStarted:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("deferred physical worktree cleanup did not start")
	}
	joined := strings.Join(commands, "\n")
	if strings.Contains(joined, "-C "+sourceWorktree+" status") {
		t.Fatalf("forced close should skip dirty source status, commands:\n%s", joined)
	}
	if !strings.Contains(joined, "worktree remove --force --force "+sourceWorktree) {
		t.Fatalf("git commands missing double-force worktree removal:\n%s", joined)
	}
	close(releaseRemove)
	select {
	case <-removeDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("deferred physical worktree cleanup did not finish after release")
	}
	record := waitForRuntimeState(t, d.operationRuntime, result.WorktreeCleanupOperationID, daemonops.StateDone)
	if record.Kind != taskDeferredWorktreeCleanupOperationKind {
		t.Fatalf("cleanup operation kind = %s, want %s", record.Kind, taskDeferredWorktreeCleanupOperationKind)
	}
}

func TestTaskCloseDeferredWorktreeCleanupCancelledWhenIssueReopens(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-deferred-reopen"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Reopen before deferred cleanup",
		Type:     domain.TypeBug,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	sourceWorktree := filepath.Join(repoDir, "wt-"+taskID)
	if err := os.MkdirAll(sourceWorktree, 0o755); err != nil {
		t.Fatalf("mkdir source worktree: %v", err)
	}
	sourceBranch := "riordan/" + taskID + "/already-integrated-dirty"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      sourceWorktree,
		Branch:    sourceBranch,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	worktreeListOutput := fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", sourceWorktree, sourceBranch)
	commands := make([]string, 0, 12)
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list":
			return worktreeListOutput, nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == repoDir && args[2] == "rev-list" && args[3] == "--count" && args[4] == "main.."+sourceBranch:
			return "0", nil
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "remove":
			return "", fmt.Errorf("cleanup should remain queued until reopened")
		default:
			return "", fmt.Errorf("unexpected git args: %s", strings.Join(args, " "))
		}
	}}

	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.operationRuntime = newOperationRuntime(operationRuntimeConfig{
		repoDir:      repoDir,
		logger:       logger,
		hub:          d.hub,
		nextRevision: d.nextRevision,
	})
	d.gitStatusAdapter = &gitServiceAdapter{
		client:            git.NewClient(runner, logger),
		runtimeStateStore: runtimeStore,
		logger:            logger,
		baseBranch:        "main",
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		logger: logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	blockerStarted := make(chan struct{})
	releaseBlocker := make(chan struct{})
	blocker, err := d.operationRuntime.manager.Submit(ctx, daemonops.SubmitRequest{
		ProjectID:    normalizedProjectID(projectID),
		IssueID:      taskID,
		Kind:         "test.blocker",
		ResourceKeys: []string{"issue:" + normalizedProjectID(projectID) + ":" + taskID},
	}, func(runCtx context.Context) ([]byte, error) {
		close(blockerStarted)
		select {
		case <-releaseBlocker:
			return []byte(`{}`), nil
		case <-runCtx.Done():
			return nil, runCtx.Err()
		}
	})
	if err != nil {
		t.Fatalf("submit blocker operation: %v", err)
	}
	select {
	case <-blockerStarted:
	case <-time.After(time.Second):
		t.Fatal("blocker operation did not start")
	}

	closeBody, err := json.Marshal(taskCloseRequest{
		TaskID:               taskID,
		ForceWorktree:        true,
		IntegrateBeforeClose: true,
	})
	if err != nil {
		t.Fatalf("marshal close request: %v", err)
	}
	closeResp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-before-reopen",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            closeBody,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if !closeResp.OK {
		if closeResp.Error != nil {
			t.Fatalf("handleTaskClose error = %s", closeResp.Error.Message)
		}
		t.Fatalf("handleTaskClose response = %+v", closeResp)
	}
	var closeResult taskCloseResult
	if err := json.Unmarshal(closeResp.Body, &closeResult); err != nil {
		t.Fatalf("unmarshal close result: %v", err)
	}
	if !closeResult.WorktreeCleanupDeferred || closeResult.WorktreeCleanupOperationID == "" {
		t.Fatalf("close result = %+v, want deferred cleanup operation", closeResult)
	}
	queued := waitForRuntimeState(t, d.operationRuntime, closeResult.WorktreeCleanupOperationID, daemonops.StateQueued)
	if queued.Kind != taskDeferredWorktreeCleanupOperationKind {
		t.Fatalf("queued operation kind = %s, want %s", queued.Kind, taskDeferredWorktreeCleanupOperationKind)
	}

	updateBody, err := json.Marshal(map[string]any{
		"task_id": taskID,
		"status":  domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("marshal update request: %v", err)
	}
	updateResp, err := d.handleTaskUpdateStatus(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-reopen-after-close",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.update_status",
		Body:            updateBody,
	})
	if err != nil {
		t.Fatalf("handleTaskUpdateStatus error: %v", err)
	}
	if !updateResp.OK {
		if updateResp.Error != nil {
			t.Fatalf("handleTaskUpdateStatus error = %s", updateResp.Error.Message)
		}
		t.Fatalf("handleTaskUpdateStatus response = %+v", updateResp)
	}
	cancelled := waitForRuntimeState(t, d.operationRuntime, closeResult.WorktreeCleanupOperationID, daemonops.StateCancelled)
	if cancelled.ErrorMessage == "" {
		t.Fatalf("cancelled cleanup error message empty")
	}
	restored, found, err := runtimeStore.GetWorktreeStateByIssueID(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("read restored worktree projection: %v", err)
	}
	if !found || restored.Path != sourceWorktree || restored.Branch != sourceBranch {
		t.Fatalf("restored projection = %+v found=%v, want %s %s", restored, found, sourceWorktree, sourceBranch)
	}
	if strings.Contains(strings.Join(commands, "\n"), "worktree remove") {
		t.Fatalf("cleanup should not remove worktree after reopen, commands:\n%s", strings.Join(commands, "\n"))
	}
	close(releaseBlocker)
	_ = waitForRuntimeState(t, d.operationRuntime, blocker.Record.ID, daemonops.StateDone)
}

func TestRecoverInterruptedDeferredWorktreeCleanupRemovesClosedIssueWorktree(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-recover-deferred-cleanup"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Recover deferred cleanup",
		Type:     domain.TypeBug,
		Priority: domain.P2,
		Status:   domain.StatusDone,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	sourceWorktree := filepath.Join(repoDir, "wt-"+taskID)
	if err := os.MkdirAll(sourceWorktree, 0o755); err != nil {
		t.Fatalf("mkdir source worktree: %v", err)
	}
	sourceBranch := "riordan/" + taskID + "/recover-deferred-cleanup"
	worktreeListOutput := fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", sourceWorktree, sourceBranch)
	commands := make([]string, 0, 8)
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list":
			return worktreeListOutput, nil
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "remove" && slices.Contains(args, "--force"):
			return "", nil
		case len(args) >= 3 && args[0] == "branch" && args[1] == "-D" && args[2] == sourceBranch:
			return "", nil
		default:
			return "", fmt.Errorf("unexpected git args: %s", strings.Join(args, " "))
		}
	}}
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(runner, repoDir, logger),
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}

	recovery, ok := d.recoverInterruptedOperation(ctx, daemonops.Record{
		ID:        "op-recover-cleanup",
		ProjectID: projectID,
		IssueID:   taskID,
		Kind:      taskDeferredWorktreeCleanupOperationKind,
		ResourceKeys: []string{
			"issue:" + normalizedProjectID(projectID) + ":" + taskID,
			"worktree:" + sourceWorktree,
			"branch:" + sourceBranch,
		},
	})
	if !ok {
		t.Fatal("recoverInterruptedOperation ok = false, want true")
	}
	if recovery.State != daemonops.StateDone {
		t.Fatalf("recovery state = %s, want done: %s", recovery.State, recovery.ErrorMessage)
	}
	joined := strings.Join(commands, "\n")
	if !strings.Contains(joined, "worktree remove --force --force "+sourceWorktree) {
		t.Fatalf("commands missing forced worktree remove:\n%s", joined)
	}
	if !strings.Contains(joined, "branch -D "+sourceBranch) {
		t.Fatalf("commands missing branch cleanup:\n%s", joined)
	}
}

func TestTaskCloseCommandRefusesChildIntegrationToBaseWithoutAncestorTarget(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-child-base"
	repoDir := t.TempDir()
	sourceWorktree := filepath.Join(repoDir, "wt-child")
	sourceBranch := "riordan/az-2/child-base"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Parent without active worktree",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Child refuses base integration",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   childID,
		Path:      sourceWorktree,
		Branch:    sourceBranch,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed child worktree projection: %v", err)
	}

	worktreeListOutput := fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", sourceWorktree, sourceBranch)
	commands := make([]string, 0, 12)
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		commands = append(commands, strings.Join(args, " "))
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list":
			return worktreeListOutput, nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "status":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "merge-base":
			return "base-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "diff":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "rev-list":
			return "0", nil
		case len(args) >= 6 && args[0] == "-C" && args[2] == "merge-tree":
			return "tree-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "fetch":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "checkout":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "merge":
			return "merge complete", nil
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "remove":
			return "", nil
		default:
			return "", nil
		}
	}}

	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		git:      git.NewClient(runner, logger),
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		logger: logger,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	body, err := json.Marshal(taskCloseRequest{
		TaskID:               childID,
		IntegrateBeforeClose: true,
	})
	if err != nil {
		t.Fatalf("marshal task close request: %v", err)
	}
	resp, err := d.handleTaskClose(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-child-base",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.close",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskClose error: %v", err)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("handleTaskClose response = %+v, want child base refusal", resp)
	}
	if !strings.Contains(resp.Error.Message, "no active ancestor worktree branch was found") ||
		!strings.Contains(resp.Error.Message, "az worktree create "+parentID) ||
		strings.Contains(resp.Error.Message, "--allow-base-for-child") {
		t.Fatalf("handleTaskClose error = %q, want parent target guidance without override suggestion", resp.Error.Message)
	}
	closed, err := issuesClient.GetWithRuntime(ctx, projectID, childID)
	if err != nil {
		t.Fatalf("get child after close: %v", err)
	}
	if closed.Status != domain.StatusInReview {
		t.Fatalf("child status = %s, want %s", closed.Status, domain.StatusInReview)
	}
	joined := strings.Join(commands, "\n")
	if strings.Contains(joined, " merge --no-edit ") || strings.Contains(joined, " merge-tree ") {
		t.Fatalf("git commands should not attempt child-to-base merge:\n%s", joined)
	}
}

func TestTaskUpdateStatusRejectsRawCloseActiveChildRuntime(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-guard-reopen-child"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Parent",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Closed child with runtime",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusInReview,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   childID,
		Path:      "/tmp/" + childID,
		Branch:    "riordan/" + childID,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed child worktree projection: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	body, err := json.Marshal(map[string]any{
		"task_id": parentID,
		"status":  domain.StatusDone,
	})
	if err != nil {
		t.Fatalf("marshal task update request: %v", err)
	}
	resp, err := d.handleTaskUpdateStatus(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-close-guard-reopen-child",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.update_status",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskUpdateStatus error: %v", err)
	}
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "status closed must be applied with task.close") {
		t.Fatalf("task.update_status response = %+v, want raw close rejection", resp)
	}
	child, err := issuesClient.GetWithRuntime(ctx, projectID, childID)
	if err != nil {
		t.Fatalf("get child after failed close: %v", err)
	}
	if child.Status != domain.StatusInReview {
		t.Fatalf("child status = %s, want %s", child.Status, domain.StatusInReview)
	}
	if strings.Contains(resp.Error.Message, "Moved closed blockers back for cleanup") {
		t.Fatalf("task.update_status response = %q, did not expect status repair for reachable active child", resp.Error.Message)
	}
}

func TestTaskGraphReadinessDependencyGating(t *testing.T) {
	root := naming.IssueID("az-root")
	a := naming.IssueID("az-a")
	b := naming.IssueID("az-b")
	aParent := root
	bParent := root

	base := []domain.Task{
		{ID: root, Type: domain.TypeEpic, Status: domain.StatusInProgress},
		{ID: a, Type: domain.TypeTask, Status: domain.StatusInProgress, ParentID: &aParent},
		{
			ID:       b,
			Type:     domain.TypeTask,
			Status:   domain.StatusOpen,
			ParentID: &bParent,
			Dependencies: []domain.Dependency{
				{ID: a, Type: domain.DependencyBlocks},
			},
		},
	}

	rootID, byID, children, err := daemonTaskGraphIndexes(root.String(), base)
	if err != nil {
		t.Fatalf("daemonTaskGraphIndexes error: %v", err)
	}
	before, err := daemonTaskGraphReadinessFromIndexes(rootID, byID, children)
	if err != nil {
		t.Fatalf("daemonTaskGraphReadinessFromIndexes before error: %v", err)
	}
	if len(before.Runnable) != 1 || before.Runnable[0] != a.String() {
		t.Fatalf("before runnable = %v, want [%s]", before.Runnable, a.String())
	}
	if got := before.Blocked[b.String()]; !strings.Contains(got, a.String()) {
		t.Fatalf("before blocked[%s] = %q, want blocker %s", b.String(), got, a.String())
	}

	after := append([]domain.Task(nil), base...)
	after[1].Status = domain.StatusDone
	rootID, byID, children, err = daemonTaskGraphIndexes(root.String(), after)
	if err != nil {
		t.Fatalf("daemonTaskGraphIndexes after error: %v", err)
	}
	gotAfter, err := daemonTaskGraphReadinessFromIndexes(rootID, byID, children)
	if err != nil {
		t.Fatalf("daemonTaskGraphReadinessFromIndexes after error: %v", err)
	}
	if len(gotAfter.Runnable) != 1 || gotAfter.Runnable[0] != b.String() {
		t.Fatalf("after runnable = %v, want [%s]", gotAfter.Runnable, b.String())
	}
}

func TestTaskGraphReadinessSkipsForeignOwnedRunnableIssues(t *testing.T) {
	root := naming.IssueID("az-root")
	leaf := naming.IssueID("az-owned")
	leafParent := root
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(time.Hour)

	tasks := []domain.Task{
		{ID: root, Type: domain.TypeEpic, Status: domain.StatusInProgress},
		{
			ID:       leaf,
			Type:     domain.TypeTask,
			Status:   domain.StatusOpen,
			ParentID: &leafParent,
			Ownership: &domain.IssueOwnership{
				OwnerID:   "agent-a",
				OwnerKind: "agent",
				ClaimedAt: now.Add(-time.Minute),
				ExpiresAt: &expiresAt,
			},
		},
	}

	rootID, byID, children, err := daemonTaskGraphIndexes(root.String(), tasks)
	if err != nil {
		t.Fatalf("daemonTaskGraphIndexes error: %v", err)
	}
	foreignActor, err := daemonTaskGraphReadinessFromIndexesForActor(rootID, byID, children, "agent-b", now)
	if err != nil {
		t.Fatalf("daemonTaskGraphReadinessFromIndexesForActor foreign actor error: %v", err)
	}
	if len(foreignActor.Runnable) != 0 {
		t.Fatalf("foreign actor runnable = %v, want empty", foreignActor.Runnable)
	}
	if got := foreignActor.Blocked[leaf.String()]; !strings.Contains(got, "owned by agent-a") {
		t.Fatalf("foreign actor blocked[%s] = %q, want ownership blocker", leaf.String(), got)
	}

	ownerActor, err := daemonTaskGraphReadinessFromIndexesForActor(rootID, byID, children, "agent-a", now)
	if err != nil {
		t.Fatalf("daemonTaskGraphReadinessFromIndexesForActor owner actor error: %v", err)
	}
	if len(ownerActor.Runnable) != 1 || ownerActor.Runnable[0] != leaf.String() {
		t.Fatalf("owner actor runnable = %v, want [%s]", ownerActor.Runnable, leaf.String())
	}

	expiredActor, err := daemonTaskGraphReadinessFromIndexesForActor(rootID, byID, children, "agent-b", expiresAt.Add(time.Second))
	if err != nil {
		t.Fatalf("daemonTaskGraphReadinessFromIndexesForActor expired actor error: %v", err)
	}
	if len(expiredActor.Runnable) != 1 || expiredActor.Runnable[0] != leaf.String() {
		t.Fatalf("expired actor runnable = %v, want [%s]", expiredActor.Runnable, leaf.String())
	}
}

func TestTaskOwnershipClaimConflictReturnsProtocolConflict(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-task-ownership-conflict"
	issuesClient := issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Owned worker",
		Type:   domain.TypeTask,
		Status: domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if _, err := issuesClient.ClaimOwnershipWithRuntime(ctx, projectID, taskID, issues.OwnershipClaimParams{OwnerID: "agent-a"}); err != nil {
		t.Fatalf("seed ownership: %v", err)
	}
	d := &Daemon{
		cfg: Config{Logger: slog.Default()},
		hub: publish.NewHub(16, 8, slog.Default()),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{},
	}
	body, err := json.Marshal(taskOwnershipRequest{TaskID: taskID, OwnerID: "agent-b"})
	if err != nil {
		t.Fatalf("marshal ownership request: %v", err)
	}

	resp, err := d.handleTaskOwnershipClaim(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-ownership-conflict",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.ownership.claim",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskOwnershipClaim error: %v", err)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("handleTaskOwnershipClaim response = %+v, want conflict error", resp)
	}
	if resp.Error.Code != protocol.ErrorCodeConflict {
		t.Fatalf("error code = %s, want %s", resp.Error.Code, protocol.ErrorCodeConflict)
	}
}

func TestTaskGraphReadinessReportsMissingDependencyAndActiveSession(t *testing.T) {
	root := naming.IssueID("az-root")
	missingLeaf := naming.IssueID("az-leaf")
	activeLeaf := naming.IssueID("az-active")
	missingParent := root
	activeParent := root
	tasks := []domain.Task{
		{ID: root, Type: domain.TypeEpic, Status: domain.StatusInProgress},
		{
			ID:       missingLeaf,
			Type:     domain.TypeTask,
			Status:   domain.StatusOpen,
			ParentID: &missingParent,
			Dependencies: []domain.Dependency{
				{ID: naming.IssueID("az-missing"), Type: domain.DependencyBlocks},
			},
		},
		{ID: activeLeaf, Type: domain.TypeTask, Status: domain.StatusInProgress, ParentID: &activeParent, HasTmuxSession: true},
	}

	rootID, byID, children, err := daemonTaskGraphIndexes(root.String(), tasks)
	if err != nil {
		t.Fatalf("daemonTaskGraphIndexes error: %v", err)
	}
	result, err := daemonTaskGraphReadinessFromIndexes(rootID, byID, children)
	if err != nil {
		t.Fatalf("daemonTaskGraphReadinessFromIndexes error: %v", err)
	}
	if len(result.Runnable) != 0 {
		t.Fatalf("runnable = %v, want empty", result.Runnable)
	}
	if got := result.Blocked[missingLeaf.String()]; !strings.Contains(got, "missing") {
		t.Fatalf("blocked[%s] = %q, want missing marker", missingLeaf.String(), got)
	}
	if len(result.Active) != 1 || result.Active[0] != activeLeaf.String() {
		t.Fatalf("active = %v, want [%s]", result.Active, activeLeaf.String())
	}
}

func TestTaskGraphReadinessStopsAtNestedRoots(t *testing.T) {
	root := naming.IssueID("az-root")
	nested := naming.IssueID("az-nested")
	grandchild := naming.IssueID("az-grandchild")
	direct := naming.IssueID("az-direct")
	nestedParent := root
	grandchildParent := nested
	directParent := root
	tasks := []domain.Task{
		{ID: root, Type: domain.TypeEpic, Status: domain.StatusInProgress},
		{ID: nested, Type: domain.TypeEpic, Status: domain.StatusOpen, ParentID: &nestedParent},
		{ID: grandchild, Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &grandchildParent},
		{ID: direct, Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &directParent},
	}

	rootID, byID, children, err := daemonTaskGraphIndexes(root.String(), tasks)
	if err != nil {
		t.Fatalf("daemonTaskGraphIndexes error: %v", err)
	}
	result, err := daemonTaskGraphReadinessFromIndexes(rootID, byID, children)
	if err != nil {
		t.Fatalf("daemonTaskGraphReadinessFromIndexes error: %v", err)
	}
	if len(result.Runnable) != 1 || result.Runnable[0] != direct.String() {
		t.Fatalf("runnable = %v, want direct child only %s", result.Runnable, direct.String())
	}
	if len(result.NestedRoots) != 1 || result.NestedRoots[0].IssueID != nested.String() {
		t.Fatalf("nested roots = %v, want %s", result.NestedRoots, nested.String())
	}
	if result.NestedRoots[0].Status != "startable" || result.NestedRoots[0].IssueStatus != string(domain.StatusOpen) {
		t.Fatalf("nested root status = %+v, want startable with issue_status=open", result.NestedRoots[0])
	}
	if result.Capacity.DirectRunnableCount != 1 || result.Capacity.NestedStartableCount != 1 {
		t.Fatalf("capacity = %+v, want direct runnable and nested startable counts", result.Capacity)
	}
	if slices.Contains(result.Runnable, grandchild.String()) {
		t.Fatalf("runnable = %v, must not flatten nested root descendant %s", result.Runnable, grandchild.String())
	}
	observations := (&Daemon{}).daemonTaskGraphWorkerObservations(context.Background(), "proj", rootID, byID, children, result)
	observedDirect := false
	for _, observation := range observations {
		if observation.IssueID == direct.String() {
			observedDirect = true
		}
		if observation.IssueID == grandchild.String() {
			t.Fatalf("worker observations included nested root descendant: %+v", observations)
		}
	}
	if !observedDirect {
		t.Fatalf("worker observations = %+v, want direct child %s", observations, direct.String())
	}
}

func TestTaskGraphReadinessLoadsRootScopedTasksWithLargeUnrelatedProject(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-root-scoped-readiness"
	issuesClient := issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Watched root",
		Type:     domain.TypeEpic,
		Priority: domain.P1,
		Status:   domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	blockerID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "External blocker",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Watched child",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := issuesClient.AddDependency(ctx, childID, blockerID, string(domain.DependencyBlocks)); err != nil {
		t.Fatalf("add blocker dependency: %v", err)
	}
	for i := 0; i < 250; i++ {
		unrelatedID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
			Title:    fmt.Sprintf("Unrelated %03d", i),
			Type:     domain.TypeTask,
			Priority: domain.P3,
			Status:   domain.StatusOpen,
		})
		if err != nil {
			t.Fatalf("create unrelated %d: %v", i, err)
		}
		if unrelatedID == rootID || unrelatedID == childID || unrelatedID == blockerID {
			t.Fatalf("unexpected duplicate unrelated id %s", unrelatedID)
		}
	}

	d := &Daemon{
		cfg: Config{Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
	}
	tasks, err := d.loadTaskGraphReadinessDomainTasks(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("loadTaskGraphReadinessDomainTasks error: %v", err)
	}
	byID := map[string]domain.Task{}
	for _, task := range tasks {
		byID[task.ID.String()] = task
	}
	if got, want := len(byID), 3; got != want {
		t.Fatalf("root-scoped task count = %d, want %d (%s, %s, %s); tasks=%v", got, want, rootID, childID, blockerID, byID)
	}
	for _, wantID := range []string{rootID, childID, blockerID} {
		if _, ok := byID[wantID]; !ok {
			t.Fatalf("root-scoped tasks missing %s: tasks=%v", wantID, byID)
		}
	}

	ready, err := d.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskGraphReadiness error: %v", err)
	}
	if got := ready.Blocked[childID]; !strings.Contains(got, blockerID) {
		t.Fatalf("blocked[%s] = %q, want blocker %s", childID, got, blockerID)
	}
	if len(ready.Runnable) != 0 {
		t.Fatalf("runnable = %v, want none while blocker is open", ready.Runnable)
	}
}

func TestTaskGraphReadinessRefreshesOnlyRootScopedSessions(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-root-scoped-session-refresh"
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Watched root",
		Type:   domain.TypeEpic,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Watched child",
		Type:     domain.TypeTask,
		Status:   domain.StatusInProgress,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	unrelatedID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Unrelated active session",
		Type:   domain.TypeTask,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create unrelated: %v", err)
	}

	childSessionID := naming.CanonicalSessionID(projectID, childID)
	unrelatedSessionID := naming.CanonicalSessionID(projectID, unrelatedID)
	seededAt := time.Date(2026, time.July, 7, 2, 0, 0, 0, time.UTC)
	for _, session := range []daemonstate.Session{
		{
			ID:            childSessionID,
			IssueID:       childID,
			State:         daemonstate.SessionStateRunning,
			ObservedState: daemonstate.SessionStateRunning,
			UpdatedAt:     seededAt,
		},
		{
			ID:            unrelatedSessionID,
			IssueID:       unrelatedID,
			State:         daemonstate.SessionStateRunning,
			ObservedState: daemonstate.SessionStateRunning,
			UpdatedAt:     seededAt,
		},
	} {
		if err := runtimeStateStore.UpsertSessionState(ctx, projectID, session); err != nil {
			t.Fatalf("seed session %s: %v", session.ID, err)
		}
	}

	tmuxRunner := newTestTmuxRunner("foreign-live-session")
	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		sessionStore: daemonstate.NewStore(),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	tasks, err := d.loadTaskGraphReadinessDomainTasks(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("loadTaskGraphReadinessDomainTasks error: %v", err)
	}
	byID := map[string]domain.Task{}
	for _, task := range tasks {
		byID[task.ID.String()] = task
	}
	if got, want := len(byID), 2; got != want {
		t.Fatalf("root-scoped task count = %d, want %d; tasks=%v", got, want, byID)
	}

	childRow, found, err := runtimeStateStore.GetSessionState(ctx, projectID, childSessionID)
	if err != nil {
		t.Fatalf("get child session: %v", err)
	}
	if !found {
		t.Fatal("child session row not found")
	}
	if childRow.ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("child observed state = %s, want %s", childRow.ObservedState, daemonstate.SessionStateStopped)
	}

	unrelatedRow, found, err := runtimeStateStore.GetSessionState(ctx, projectID, unrelatedSessionID)
	if err != nil {
		t.Fatalf("get unrelated session: %v", err)
	}
	if !found {
		t.Fatal("unrelated session row not found")
	}
	if unrelatedRow.ObservedState != daemonstate.SessionStateRunning {
		t.Fatalf("unrelated observed state = %s, want unchanged %s", unrelatedRow.ObservedState, daemonstate.SessionStateRunning)
	}
	if !unrelatedRow.UpdatedAt.Equal(seededAt) {
		t.Fatalf("unrelated updated_at = %v, want unchanged %v", unrelatedRow.UpdatedAt, seededAt)
	}
}

func TestTaskClosePreflightLoadsRootScopedSubtreeWithLargeUnrelatedProject(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-close-scoped-subtree"
	issuesClient := issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Close root",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Close child",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	grandchildID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Close grandchild",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusOpen,
		ParentID: &childID,
	})
	if err != nil {
		t.Fatalf("create grandchild: %v", err)
	}
	for i := 0; i < 250; i++ {
		unrelatedID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
			Title:    fmt.Sprintf("Unrelated %03d", i),
			Type:     domain.TypeTask,
			Priority: domain.P3,
			Status:   domain.StatusOpen,
		})
		if err != nil {
			t.Fatalf("create unrelated %d: %v", i, err)
		}
		if unrelatedID == rootID || unrelatedID == childID || unrelatedID == grandchildID {
			t.Fatalf("unexpected duplicate unrelated id %s", unrelatedID)
		}
	}

	d := &Daemon{
		cfg: Config{Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
	}
	tasks, err := d.loadTaskClosePreflightDomainTasks(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("loadTaskClosePreflightDomainTasks error: %v", err)
	}
	byID := map[string]domain.Task{}
	for _, task := range tasks {
		byID[task.ID.String()] = task
	}
	if got, want := len(byID), 3; got != want {
		t.Fatalf("close preflight scoped task count = %d, want %d (%s, %s, %s); tasks=%v", got, want, rootID, childID, grandchildID, byID)
	}
	for _, wantID := range []string{rootID, childID, grandchildID} {
		if _, ok := byID[wantID]; !ok {
			t.Fatalf("close preflight scoped tasks missing %s: tasks=%v", wantID, byID)
		}
	}

	guard := daemonCloseGuardChildBlockers(naming.IssueID(rootID), tasks, taskClosePreflightOptions{})
	if len(guard) != 1 || !strings.Contains(guard[0], childID+" (open)") || !strings.Contains(guard[0], grandchildID+" (open)") {
		t.Fatalf("close child guard = %+v, want root descendants only", guard)
	}
}

func TestTaskGraphReadinessSurfacesPendingSessionStartProgress(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	issuesClient := issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Root",
		Type:     domain.TypeEpic,
		Priority: domain.P1,
		Status:   domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Worker",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}

	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir, nextRevision: sequentialRevision()})
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	if _, err := runtime.manager.Submit(ctx, daemonops.SubmitRequest{
		ID:           "op-session-start",
		ProjectID:    projectID,
		IssueID:      childID,
		Kind:         daemonhandlers.CommandSessionStart,
		ResourceKeys: []string{"issue:" + projectID + ":" + childID},
	}, func(ctx context.Context) ([]byte, error) {
		_ = daemonops.ReportProgress(ctx, daemonops.Progress{
			Phase:   "worktree_preflight",
			Message: "creating or reusing worktree",
			Current: 25,
			Total:   100,
			Unit:    "percent",
			Percent: 25,
		})
		close(started)
		<-release
		return nil, nil
	}); err != nil {
		t.Fatalf("submit session start operation: %v", err)
	}
	<-started
	waitForRuntimeProgress(t, runtime, "op-session-start", "worktree_preflight")

	daemon := &Daemon{
		cfg:              Config{RepoDir: repoDir, Logger: slog.Default()},
		operationRuntime: runtime,
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
	}
	ready, err := daemon.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskGraphReadiness error: %v", err)
	}
	if len(ready.Runnable) != 0 {
		t.Fatalf("runnable = %+v, want pending session start removed", ready.Runnable)
	}
	if len(ready.SessionStartProgress) != 1 {
		t.Fatalf("session_start_progress = %+v, want one entry", ready.SessionStartProgress)
	}
	progress := ready.SessionStartProgress[0]
	if progress.IssueID != childID || progress.OperationID != "op-session-start" || progress.Phase != "worktree_preflight" || progress.Percent != 25 {
		t.Fatalf("session start progress = %+v", progress)
	}
}

func TestTaskGraphReadinessPendingStartOverridesStaleCloseableProjection(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-pending-start-overrides-stale"
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(dbPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(dbPath, logger)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Root",
		Type:   domain.TypeEpic,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Starting child",
		Type:     domain.TypeTask,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := runtimeStateStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   childID,
		Path:      filepath.Join(t.TempDir(), "repo-"+childID),
		Branch:    "riordan/" + childID + "/task",
		UpdatedAt: time.Date(2026, time.July, 7, 9, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed stale worktree projection: %v", err)
	}

	runtime := newOperationRuntime(operationRuntimeConfig{
		repoDir: filepath.Dir(dbPath),
		logger:  logger,
		hub:     publish.NewHub(16, 8, logger),
	})
	t.Cleanup(func() { _ = runtime.Close() })
	if _, err := runtime.store.Create(ctx, opstore.CreateParams{
		OperationID:  "op-session-start",
		ProjectID:    projectID,
		IssueID:      childID,
		Kind:         daemonhandlers.CommandSessionStart,
		DedupeKey:    "session.start:" + childID,
		ResourceKeys: []string{"issue:" + projectID + ":" + childID},
		State:        opstore.StateRunning,
		SubmittedAt:  time.Date(2026, time.July, 7, 9, 1, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed running session.start operation: %v", err)
	}

	d := &Daemon{
		cfg:              Config{RepoDir: ".", Logger: logger},
		operationRuntime: runtime,
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	ready, err := d.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskGraphReadiness error: %v", err)
	}
	if slices.Contains(ready.Runnable, childID) {
		t.Fatalf("pending child still runnable: %+v", ready)
	}
	if len(ready.StaleCloseableChildren) != 0 {
		t.Fatalf("stale closeable children = %+v, want none while start is running", ready.StaleCloseableChildren)
	}
	if len(ready.Pending) != 1 || ready.Pending[0].IssueID != childID || ready.Pending[0].OperationID != "op-session-start" {
		t.Fatalf("pending = %+v, want running start for %s", ready.Pending, childID)
	}
}

func TestTaskGraphReadinessStoppedProjectionSuppressesNewerSnapshotAfterClose(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-close-stopped-projection-wins"
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(dbPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(dbPath, logger)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Root",
		Type:   domain.TypeEpic,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Closed child",
		Type:     domain.TypeTask,
		Status:   domain.StatusDone,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create closed child: %v", err)
	}

	sessionID := naming.CanonicalSessionID(projectID, childID)
	stoppedAt := time.Date(2026, time.July, 7, 9, 0, 0, 0, time.UTC)
	if err := runtimeStateStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID:            sessionID,
		IssueID:       childID,
		State:         daemonstate.SessionStateStopped,
		ObservedState: daemonstate.SessionStateStopped,
		UpdatedAt:     stoppedAt,
	}); err != nil {
		t.Fatalf("seed stopped projection: %v", err)
	}

	sessionStore := daemonstate.NewStore()
	if _, err := sessionStore.UpsertSession(projectID, sessionID, childID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed in-memory session: %v", err)
	}
	snapshot := sessionStore.ReadSnapshot(projectID)
	session := snapshot.Sessions[sessionID]
	session.UpdatedAt = stoppedAt.Add(time.Minute)
	sessionStore.ReplaceProjectSessions(projectID, []daemonstate.Session{session})

	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: logger},
		sessionStore: sessionStore,
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	ready, err := d.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskGraphReadiness error: %v", err)
	}
	if len(ready.Runnable) != 0 {
		t.Fatalf("runnable = %+v, want none for closed child", ready.Runnable)
	}
	if len(ready.Active) != 0 || len(ready.ActiveSessions) != 0 {
		t.Fatalf("closed child resurrected as active: active=%+v active_sessions=%+v", ready.Active, ready.ActiveSessions)
	}
	if len(ready.StaleCloseableChildren) != 0 {
		t.Fatalf("stale closeable children = %+v, want none for closed child", ready.StaleCloseableChildren)
	}
}

func TestTaskCompletionAdviceIncludesDomainNextSteps(t *testing.T) {
	advice := daemonTaskCompletionAdvice("az-1", []string{"az-2"}, []taskGraphNestedRoot{{IssueID: "az-5"}}, []string{"az-3"}, []string{"az-4"}, nil)
	joined := strings.Join(advice, "\n")
	for _, want := range []string{
		"az orchestrate close-session --issue az-4",
		"az session start az-5",
		"az issue close --id az-3",
		"az orchestrate start --root az-1 --issue az-2 --json",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("advice = %+v, missing %q", advice, want)
		}
	}
}

func TestTaskGraphCapacitySummaryDedupesNestedStartProgress(t *testing.T) {
	ready := taskGraphReadinessResult{
		Active:               []string{"az-direct"},
		SessionStartProgress: []taskGraphSessionStartProgress{{IssueID: "az-nested", OperationID: "op-1"}},
		NestedRoots:          []taskGraphNestedRoot{{IssueID: "az-nested", Status: "active"}},
	}
	capacity := daemonTaskGraphCapacitySummary(ready)
	if capacity.DirectActiveCount != 1 || capacity.PendingStartsCount != 1 || capacity.NestedActiveCount != 1 {
		t.Fatalf("capacity counts = %+v, want direct/pending/nested each reported separately", capacity)
	}
	if capacity.TotalCountingCapacityCount != 2 {
		t.Fatalf("total counting capacity = %d, want unique issue count 2", capacity.TotalCountingCapacityCount)
	}
}

func TestTaskGraphReadinessSurfacesStaleCloseableChild(t *testing.T) {
	root := naming.IssueID("az-root")
	child := naming.IssueID("az-child")
	childParent := root
	tasks := []domain.Task{
		{ID: root, Type: domain.TypeEpic, Status: domain.StatusInProgress},
		{
			ID:            child,
			Type:          domain.TypeTask,
			Status:        domain.StatusOpen,
			ParentID:      &childParent,
			HasWorktree:   true,
			GitAheadCount: 0,
		},
	}

	rootID, byID, children, err := daemonTaskGraphIndexes(root.String(), tasks)
	if err != nil {
		t.Fatalf("daemonTaskGraphIndexes error: %v", err)
	}
	result, err := daemonTaskGraphReadinessFromIndexes(rootID, byID, children)
	if err != nil {
		t.Fatalf("daemonTaskGraphReadinessFromIndexes error: %v", err)
	}
	if slices.Contains(result.Runnable, child.String()) {
		t.Fatalf("stale-closeable child should not be runnable: %+v", result)
	}
	if len(result.StaleCloseableChildren) != 1 {
		t.Fatalf("stale_closeable_children = %+v, want one candidate", result.StaleCloseableChildren)
	}
	candidate := result.StaleCloseableChildren[0]
	if candidate.IssueID != child.String() || candidate.SuggestedCommand != "az issue close --id az-root --close-clean-children" {
		t.Fatalf("candidate = %+v", candidate)
	}
	joinedEvidence := strings.Join(candidate.Evidence, "\n")
	for _, want := range []string{"no active session", "clean worktree", "branch not ahead", "status=open"} {
		if !strings.Contains(joinedEvidence, want) {
			t.Fatalf("candidate evidence = %+v, missing %q", candidate.Evidence, want)
		}
	}
}

func TestTaskGraphReadinessReportsStaleChildBranchContainmentRisk(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-containment-risk"
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(dbPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(dbPath, logger)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	rootIDRaw, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Parent", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	rootID := naming.IssueID(rootIDRaw)
	closedIDRaw, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Closed child", Type: domain.TypeTask, Status: domain.StatusDone, ParentID: &rootIDRaw})
	if err != nil {
		t.Fatalf("create closed child: %v", err)
	}
	closedID := naming.IssueID(closedIDRaw)
	activeIDRaw, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Active reconciliation", Type: domain.TypeBug, Status: domain.StatusInProgress, ParentID: &rootIDRaw})
	if err != nil {
		t.Fatalf("create active child: %v", err)
	}
	activeID := naming.IssueID(activeIDRaw)
	rootBranch := "riordan/" + rootID.String() + "/profile-and-worker-mater-cif-merge"
	activeBranch := "riordan/" + activeID.String() + "/reconcile"

	repoDir := t.TempDir()
	runDaemonTestGit(t, repoDir, "init", "-q", "-b", "main")
	runDaemonTestGit(t, repoDir, "config", "user.email", "test@example.com")
	runDaemonTestGit(t, repoDir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repoDir, "rpc.go"), []byte("package rpc\n\nfunc materialize() string { return \"base\" }\n"), 0o644); err != nil {
		t.Fatalf("write base file: %v", err)
	}
	runDaemonTestGit(t, repoDir, "add", "rpc.go")
	runDaemonTestGit(t, repoDir, "commit", "-q", "-m", "base")
	runDaemonTestGit(t, repoDir, "checkout", "-q", "-b", activeBranch)
	if err := os.WriteFile(filepath.Join(repoDir, "rpc.go"), []byte("package rpc\n\nfunc materialize() string { return \"generic\" }\n"), 0o644); err != nil {
		t.Fatalf("write active file: %v", err)
	}
	runDaemonTestGit(t, repoDir, "commit", "-am", activeID.String()+": keep generic materializer")
	runDaemonTestGit(t, repoDir, "checkout", "-q", "main")
	runDaemonTestGit(t, repoDir, "checkout", "-q", "-b", rootBranch)
	if err := os.WriteFile(filepath.Join(repoDir, "rpc.go"), []byte("package rpc\n\nfunc materializeTyped() string { return \"typed\" }\n"), 0o644); err != nil {
		t.Fatalf("write evidence file: %v", err)
	}
	runDaemonTestGit(t, repoDir, "commit", "-am", closedID.String()+": generate typed materializer rpc")
	evidenceCommit := runDaemonTestGitOutput(t, repoDir, "rev-parse", "HEAD")

	for _, state := range []daemonstate.WorktreeState{
		{ProjectID: projectID, IssueID: rootID.String(), Path: repoDir, Branch: rootBranch, UpdatedAt: time.Now().UTC()},
		{ProjectID: projectID, IssueID: activeID.String(), Path: repoDir, Branch: activeBranch, UpdatedAt: time.Now().UTC()},
	} {
		if err := runtimeStateStore.UpsertWorktreeState(ctx, state); err != nil {
			t.Fatalf("seed worktree state: %v", err)
		}
	}

	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: logger},
		git: git.NewClient(git.NewExecRunner(repoDir), logger),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
		worktreeAdapter: &worktreeServiceAdapter{
			manager:           git.NewWorktreeManager(git.NewExecRunner(repoDir), repoDir, logger),
			runtimeStateStore: runtimeStateStore,
			logger:            logger,
		},
	}

	ready, err := d.taskGraphReadiness(ctx, projectID, rootID.String())
	if err != nil {
		t.Fatalf("taskGraphReadiness error: %v", err)
	}
	if len(ready.ContainmentRisks) != 1 {
		t.Fatalf("containment risks = %+v, want one stale child risk", ready.ContainmentRisks)
	}
	risk := ready.ContainmentRisks[0]
	if risk.Classification != "stale_child_branch" || risk.IssueID != activeID.String() || risk.ClosedChildIssueID != closedID.String() {
		t.Fatalf("risk identity = %+v, want stale child risk for active %s closed %s", risk, activeID, closedID)
	}
	if !risk.RootContainsEvidence || risk.ActiveContainsEvidence {
		t.Fatalf("risk containment = root:%t active:%t, want root true active false", risk.RootContainsEvidence, risk.ActiveContainsEvidence)
	}
	if risk.EvidenceCommit != evidenceCommit {
		t.Fatalf("evidence commit = %s, want %s", risk.EvidenceCommit, evidenceCommit)
	}
	if !slices.Contains(risk.OverlapFiles, "rpc.go") {
		t.Fatalf("overlap files = %+v, want rpc.go", risk.OverlapFiles)
	}
	if !strings.Contains(risk.Message, "stale child branch") || !strings.Contains(risk.Message, "parent branch") {
		t.Fatalf("risk message = %q, want stale child branch parent wording", risk.Message)
	}
}

func runDaemonTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
}

func runDaemonTestGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
}

func TestTaskGraphReadinessDoesNotMisreportIncompleteChildAsCloseable(t *testing.T) {
	root := naming.IssueID("az-root")
	child := naming.IssueID("az-child")
	childParent := root
	tasks := []domain.Task{
		{ID: root, Type: domain.TypeEpic, Status: domain.StatusInProgress},
		{ID: child, Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &childParent},
	}

	rootID, byID, children, err := daemonTaskGraphIndexes(root.String(), tasks)
	if err != nil {
		t.Fatalf("daemonTaskGraphIndexes error: %v", err)
	}
	result, err := daemonTaskGraphReadinessFromIndexes(rootID, byID, children)
	if err != nil {
		t.Fatalf("daemonTaskGraphReadinessFromIndexes error: %v", err)
	}
	if len(result.StaleCloseableChildren) != 0 {
		t.Fatalf("stale_closeable_children = %+v, want none", result.StaleCloseableChildren)
	}
	if !slices.Contains(result.Runnable, child.String()) {
		t.Fatalf("runnable = %+v, want incomplete child runnable", result.Runnable)
	}
}

func TestTaskGraphReadinessReportsNestedTrackerRootInsteadOfFlatteningDescendants(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	projectID := "proj-nested-tracker-readiness"
	issuesClient := issues.NewClient(repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Outer root",
		Type:   domain.TypeEpic,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	directID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Direct worker",
		Type:     domain.TypeTask,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create direct worker: %v", err)
	}
	trackerID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Nested tracker",
		Type:     domain.TypeTask,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create tracker: %v", err)
	}
	grandchildID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Tracker child",
		Type:     domain.TypeTask,
		Status:   domain.StatusOpen,
		ParentID: &trackerID,
	})
	if err != nil {
		t.Fatalf("create tracker child: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		sessionStore: daemonstate.NewStore(),
	}
	ready, err := d.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskGraphReadiness error: %v", err)
	}
	if !slices.Contains(ready.Runnable, directID) {
		t.Fatalf("runnable = %+v, want direct worker %s", ready.Runnable, directID)
	}
	if slices.Contains(ready.Runnable, grandchildID) {
		t.Fatalf("runnable = %+v, did not expect nested child %s under outer root", ready.Runnable, grandchildID)
	}
	if len(ready.NestedRoots) != 1 {
		t.Fatalf("nested_roots = %+v, want one nested tracker", ready.NestedRoots)
	}
	nested := ready.NestedRoots[0]
	if nested.IssueID != trackerID || nested.Status != "startable" || nested.IssueStatus != string(domain.StatusOpen) || nested.Type != string(domain.TypeTask) || nested.ChildCount != 1 {
		t.Fatalf("nested root = %+v, want tracker %s", nested, trackerID)
	}
	if ready.Capacity.DirectRunnableCount != 1 || ready.Capacity.NestedStartableCount != 1 || ready.Capacity.TotalCountingCapacityCount != 0 {
		t.Fatalf("capacity = %+v, want direct runnable and nested startable outside active capacity", ready.Capacity)
	}
	if !strings.Contains(nested.Advice, "az session start "+trackerID) {
		t.Fatalf("nested advice = %q, want session start guidance", nested.Advice)
	}
	for _, observation := range ready.WorkerObservations {
		if observation.IssueID == grandchildID {
			t.Fatalf("worker observations included nested child under outer root: %+v", ready.WorkerObservations)
		}
	}
}

func TestTaskGraphReadinessMarksFailedNestedRootStartAsBlockedCapacity(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	repoDir := t.TempDir()
	projectID := "proj-nested-start-failed"
	issuesClient := issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Outer root",
		Type:   domain.TypeEpic,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	nestedID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Nested root",
		Type:     domain.TypeEpic,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create nested root: %v", err)
	}
	if _, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Nested child",
		Type:     domain.TypeTask,
		Status:   domain.StatusOpen,
		ParentID: &nestedID,
	}); err != nil {
		t.Fatalf("create nested child: %v", err)
	}

	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir, nextRevision: sequentialRevision()})
	if _, err := runtime.manager.Submit(ctx, daemonops.SubmitRequest{
		ID:           "op-nested-start",
		ProjectID:    projectID,
		IssueID:      nestedID,
		Kind:         daemonhandlers.CommandSessionStart,
		ResourceKeys: []string{"issue:" + projectID + ":" + nestedID},
	}, func(context.Context) ([]byte, error) {
		return nil, errors.New("worktree setup failed")
	}); err != nil {
		t.Fatalf("submit failed session start operation: %v", err)
	}
	_ = waitForRuntimeState(t, runtime, "op-nested-start", daemonops.StateFailed)

	d := &Daemon{
		cfg:              Config{RepoDir: repoDir, Logger: logger},
		operationRuntime: runtime,
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
	}
	ready, err := d.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskGraphReadiness error: %v", err)
	}
	if len(ready.NestedRoots) != 1 {
		t.Fatalf("nested_roots = %+v, want one nested root", ready.NestedRoots)
	}
	nested := ready.NestedRoots[0]
	if nested.Status != "blocked_start_failed" || nested.StartFailure == nil || nested.StartFailure.OperationID != "op-nested-start" {
		t.Fatalf("nested root = %+v, want blocked_start_failed with operation", nested)
	}
	if nested.FallbackPolicy != "keep_children_blocked_or_create_replacement_direct_work" {
		t.Fatalf("fallback policy = %q", nested.FallbackPolicy)
	}
	if !strings.Contains(nested.Advice, "retry `az session start "+nestedID+"`") || !strings.Contains(nested.Advice, "replacement direct work") {
		t.Fatalf("advice = %q, want retry and replacement guidance", nested.Advice)
	}
	if ready.Capacity.BlockedNestedRootsCount != 1 || ready.Capacity.NestedStartableCount != 0 {
		t.Fatalf("capacity = %+v, want blocked nested root count", ready.Capacity)
	}

	if _, err := runtime.manager.Submit(ctx, daemonops.SubmitRequest{
		ID:           "op-nested-start-retry",
		ProjectID:    projectID,
		IssueID:      nestedID,
		Kind:         daemonhandlers.CommandSessionStart,
		ResourceKeys: []string{"issue:" + projectID + ":" + nestedID},
	}, func(context.Context) ([]byte, error) {
		return []byte(`{"ok":true}`), nil
	}); err != nil {
		t.Fatalf("submit successful retry session start operation: %v", err)
	}
	_ = waitForRuntimeState(t, runtime, "op-nested-start-retry", daemonops.StateDone)
	ready, err = d.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskGraphReadiness after retry error: %v", err)
	}
	nested = ready.NestedRoots[0]
	if nested.Status != "startable" || nested.StartFailure != nil {
		t.Fatalf("nested root after successful retry = %+v, want startable without stale failure", nested)
	}
}

func TestTaskCompleteCheckIgnoresClosedNestedRootWithClosedDescendants(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	projectID := "proj-complete-closed-nested-root"
	issuesClient := issues.NewClient(repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	trackerID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Closed tracker", Type: domain.TypeTask, Status: domain.StatusDone, ParentID: &rootID})
	if err != nil {
		t.Fatalf("create tracker: %v", err)
	}
	if _, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Closed child", Type: domain.TypeTask, Status: domain.StatusDone, ParentID: &trackerID}); err != nil {
		t.Fatalf("create tracker child: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		sessionStore: daemonstate.NewStore(),
	}
	ready, err := d.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskGraphReadiness error: %v", err)
	}
	if len(ready.NestedRoots) != 0 {
		t.Fatalf("nested_roots = %+v, want none for closed nested subtree", ready.NestedRoots)
	}
	result, err := d.taskCompleteCheck(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskCompleteCheck error: %v", err)
	}
	if !result.Pass {
		t.Fatalf("complete check = %+v, want pass for closed nested subtree", result)
	}
}

func TestWorkerObservationStateDerivationPrecedence(t *testing.T) {
	rootID := "az-root"
	baseTask := func(id string, status domain.Status) domain.Task {
		return domain.Task{
			ID:       naming.IssueID(id),
			Type:     domain.TypeTask,
			Status:   status,
			Priority: domain.P2,
		}
	}

	tests := []struct {
		name string
		in   workerObservationInputs
		want domain.WorkerObservationState
	}{
		{
			name: "done with runtime cleanup pending wins over active",
			in: workerObservationInputs{
				RootIssueID: rootID,
				Task: func() domain.Task {
					task := baseTask("az-cleanup", domain.StatusDone)
					task.HasTmuxSession = true
					return task
				}(),
				Active: &taskGraphActiveSession{IssueID: "az-cleanup", Activity: "busy", Status: "active"},
			},
			want: domain.WorkerObservationCleanupPending,
		},
		{
			name: "plain done",
			in: workerObservationInputs{
				RootIssueID: rootID,
				Task:        baseTask("az-done", domain.StatusDone),
			},
			want: domain.WorkerObservationDone,
		},
		{
			name: "failed active session wins over review",
			in: workerObservationInputs{
				RootIssueID: rootID,
				Task:        baseTask("az-failed", domain.StatusInReview),
				Active:      &taskGraphActiveSession{IssueID: "az-failed", Activity: "failed", Status: "active"},
			},
			want: domain.WorkerObservationFailed,
		},
		{
			name: "waiting active session",
			in: workerObservationInputs{
				RootIssueID: rootID,
				Task:        baseTask("az-waiting", domain.StatusInProgress),
				Active:      &taskGraphActiveSession{IssueID: "az-waiting", Activity: "waiting", Status: "active"},
			},
			want: domain.WorkerObservationWaitingHuman,
		},
		{
			name: "working active session",
			in: workerObservationInputs{
				RootIssueID: rootID,
				Task:        baseTask("az-working", domain.StatusInProgress),
				Active:      &taskGraphActiveSession{IssueID: "az-working", Activity: "busy", Status: "active"},
			},
			want: domain.WorkerObservationWorking,
		},
		{
			name: "review beats blocked",
			in: workerObservationInputs{
				RootIssueID:   rootID,
				Task:          baseTask("az-review", domain.StatusInReview),
				BlockedReason: "waiting on az-blocker",
			},
			want: domain.WorkerObservationReviewReady,
		},
		{
			name: "blocked",
			in: workerObservationInputs{
				RootIssueID:   rootID,
				Task:          baseTask("az-blocked", domain.StatusOpen),
				BlockedReason: "waiting on az-blocker",
			},
			want: domain.WorkerObservationBlocked,
		},
		{
			name: "stale closeable",
			in: workerObservationInputs{
				RootIssueID: rootID,
				Task:        baseTask("az-stale", domain.StatusOpen),
				Stale:       &taskStaleCloseableCandidate{IssueID: "az-stale", Evidence: []string{"clean worktree"}},
			},
			want: domain.WorkerObservationStale,
		},
		{
			name: "pending start",
			in: workerObservationInputs{
				RootIssueID: rootID,
				Task:        baseTask("az-pending", domain.StatusOpen),
				Pending:     &taskGraphPendingStart{IssueID: "az-pending", OperationID: "op-1", OperationState: "queued"},
			},
			want: domain.WorkerObservationWorking,
		},
		{
			name: "runnable",
			in: workerObservationInputs{
				RootIssueID: rootID,
				Task:        baseTask("az-runnable", domain.StatusOpen),
				Runnable:    true,
			},
			want: domain.WorkerObservationRunnable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := daemonWorkerObservationFromInputs(tt.in)
			if got.State != tt.want {
				t.Fatalf("state = %q, want %q (observation=%+v)", got.State, tt.want, got)
			}
			if got.SourceTruthPolicy.IssueGraph != string(daemonInvariantSourceProjection) ||
				got.SourceTruthPolicy.SessionRuntime != string(daemonInvariantSourceHybrid) ||
				got.SourceTruthPolicy.MailboxEvidence != string(daemonInvariantSourceProjection) {
				t.Fatalf("source policy = %+v", got.SourceTruthPolicy)
			}
			if got.Reason == "" && got.State != domain.WorkerObservationDone {
				t.Fatalf("reason missing for %+v", got)
			}
		})
	}
}

func TestLatestWorkerObservationIssueEventFiltersStructuralDependencies(t *testing.T) {
	blocking := domain.IssueObservationEvent{
		ID:         1,
		Type:       domain.IssueEventIssueDependencyAdded,
		ObservedAt: time.Date(2026, time.June, 17, 8, 0, 0, 0, time.UTC),
		Source:     "issue-store",
		Payload:    map[string]any{"dependency_type": string(domain.DependencyBlocks)},
	}
	events := []domain.IssueObservationEvent{
		blocking,
		{
			ID:         2,
			Type:       domain.IssueEventIssueDependencyAdded,
			ObservedAt: blocking.ObservedAt.Add(time.Minute),
			Source:     "issue-store",
			Payload:    map[string]any{"dependency_type": string(domain.DependencyParentChild)},
		},
		{
			ID:         3,
			Type:       domain.IssueEventIssueDependencyAdded,
			ObservedAt: blocking.ObservedAt.Add(2 * time.Minute),
			Source:     "issue-store",
			Payload:    map[string]any{"dependency_type": string(domain.DependencyCreatedIn)},
		},
	}

	got := latestWorkerObservationIssueEvent(events)
	if got == nil || got.ID != blocking.ID {
		t.Fatalf("latestWorkerObservationIssueEvent() = %+v, want blocking dependency event", got)
	}
}

func TestTaskGraphReadinessWorkerObservationsIncludeEvidenceAndMissingProjectionStates(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	projectID := "proj-worker-observation"
	issuesClient := issues.NewClient(repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Root",
		Type:   domain.TypeEpic,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	runnableID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Runnable worker",
		Type:     domain.TypeTask,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create runnable: %v", err)
	}
	blockerID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Blocker",
		Type:     domain.TypeTask,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	blockedID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Blocked worker",
		Type:     domain.TypeTask,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create blocked: %v", err)
	}
	if err := issuesClient.AddDependency(ctx, blockedID, blockerID, string(domain.DependencyBlocks)); err != nil {
		t.Fatalf("add blocker dependency: %v", err)
	}
	reviewID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Review worker",
		Type:     domain.TypeTask,
		Status:   domain.StatusInReview,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create review: %v", err)
	}
	closedID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Closed worker",
		Type:     domain.TypeTask,
		Status:   domain.StatusDone,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create closed: %v", err)
	}

	if _, err := issuesClient.AppendIssueObservationEvent(ctx, reviewID, issues.IssueObservationEventParams{
		Type:       domain.IssueEventEvidenceSubmitted,
		ObservedAt: time.Date(2026, time.June, 17, 9, 0, 0, 0, time.UTC),
		Source:     "test",
		Payload:    map[string]any{"summary": "focused validation passed"},
	}); err != nil {
		t.Fatalf("append review event: %v", err)
	}
	if err := appendMailboxEvent(repoDir, daemonMailEvent{
		Seq:         7,
		ParentIssue: rootID,
		IssueID:     reviewID,
		Type:        "worker-integration-ready",
		Body:        "ready with commands and files changed",
		CreatedAt:   time.Date(2026, time.July, 6, 10, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("append mailbox event: %v", err)
	}

	sessionStore := daemonstate.NewStore()
	closedSessionID := naming.CanonicalSessionID(projectID, closedID)
	if _, err := sessionStore.UpsertSession(projectID, closedSessionID, closedID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed closed session: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		sessionStore: sessionStore,
	}
	ready, err := d.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskGraphReadiness error: %v", err)
	}
	observations := map[string]domain.WorkerObservation{}
	for _, observation := range ready.WorkerObservations {
		observations[observation.IssueID] = observation
	}
	for _, wantID := range []string{runnableID, blockerID, blockedID, reviewID, closedID} {
		if _, ok := observations[wantID]; !ok {
			t.Fatalf("worker observations missing %s: %+v", wantID, ready.WorkerObservations)
		}
	}
	if got := observations[runnableID]; got.State != domain.WorkerObservationRunnable {
		t.Fatalf("runnable observation = %+v", got)
	}
	if got := observations[blockedID]; got.State != domain.WorkerObservationBlocked || !strings.Contains(got.Reason, blockerID) {
		t.Fatalf("blocked observation = %+v", got)
	}
	if got := observations[reviewID]; got.State != domain.WorkerObservationReviewReady || got.LastEvent == nil {
		t.Fatalf("review observation = %+v", got)
	}
	if got := strings.Join(observations[reviewID].EvidenceSummary, "\n"); !strings.Contains(got, "worker-integration-ready") || !strings.Contains(got, "evidence.submitted") {
		t.Fatalf("review evidence = %q", got)
	} else if strings.Contains(got, "issue.dependency_added") || strings.Contains(got, "parent-child") {
		t.Fatalf("review evidence includes structural dependency event: %q", got)
	}
	if got := observations[closedID]; got.State != domain.WorkerObservationCleanupPending {
		t.Fatalf("closed observation = %+v", got)
	}
}

func TestTaskGraphReadinessWorkerObservationsIncludeNonEpicRootLeaf(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	projectID := "proj-root-leaf-observation"
	issuesClient := issues.NewClient(repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Ordinary active work",
		Type:   domain.TypeTask,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create root task: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		sessionStore: daemonstate.NewStore(),
	}
	ready, err := d.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskGraphReadiness error: %v", err)
	}
	if len(ready.WorkerObservations) != 1 {
		t.Fatalf("worker observations = %+v, want root leaf observation", ready.WorkerObservations)
	}
	observation := ready.WorkerObservations[0]
	if observation.IssueID != rootID || observation.State != domain.WorkerObservationRunnable {
		t.Fatalf("root observation = %+v", observation)
	}
	if !slices.Contains(ready.Runnable, rootID) {
		t.Fatalf("runnable = %+v, want root leaf", ready.Runnable)
	}
}

func TestTaskCompleteCheckReportsMixedStaleCloseableAndIncompleteChildren(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-complete-check-stale-closeable"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	staleID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Stale child", Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &rootID})
	if err != nil {
		t.Fatalf("create stale child: %v", err)
	}
	incompleteID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Incomplete child", Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &rootID})
	if err != nil {
		t.Fatalf("create incomplete child: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   staleID,
		Path:      filepath.Join(repoDir, "wt-"+staleID),
		Branch:    "riordan/" + staleID + "/stale",
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed stale child worktree projection: %v", err)
	}
	cleanStatus, err := json.Marshal(git.GitStatus{HasChanges: false, GitAheadCount: 0})
	if err != nil {
		t.Fatalf("marshal clean git status: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeStateGitStatus(ctx, projectID, staleID, cleanStatus, time.Now().UTC()); err != nil {
		t.Fatalf("seed stale child git status: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 1},
	}

	result, err := d.taskCompleteCheck(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskCompleteCheck error: %v", err)
	}
	if result.Pass {
		t.Fatalf("complete check unexpectedly passed: %+v", result)
	}
	if len(result.StaleCloseableChildren) != 1 || result.StaleCloseableChildren[0].IssueID != staleID {
		t.Fatalf("stale_closeable_children = %+v, want %s", result.StaleCloseableChildren, staleID)
	}
	reasons := strings.Join(result.Reasons, "\n")
	if !strings.Contains(reasons, "stale-closeable child candidates remain: "+staleID) {
		t.Fatalf("reasons = %+v, missing stale-closeable reason", result.Reasons)
	}
	if !strings.Contains(reasons, "required descendants not closed: "+incompleteID) || strings.Contains(reasons, "required descendants not closed: "+staleID) {
		t.Fatalf("reasons = %+v, want incomplete only in required descendants", result.Reasons)
	}
	advice := strings.Join(result.Advice, "\n")
	if !strings.Contains(advice, "az issue close --id "+rootID+" --close-clean-children") {
		t.Fatalf("advice = %+v, missing close-clean-children parent command", result.Advice)
	}
	if !strings.Contains(advice, "az orchestrate start --root "+rootID+" --issue "+incompleteID+" --json") {
		t.Fatalf("advice = %+v, missing runnable incomplete child start command", result.Advice)
	}
}

func TestTaskIntegrationReadinessAcceptsLegacyMailboxAliases(t *testing.T) {
	tests := map[string]bool{
		"worker-integration-ready":       true,
		" worker-integration-ready ":     true,
		"WORKER-INTEGRATION-READY":       true,
		"worker-ready":                   true,
		" worker-ready ":                 true,
		"worker-complete":                true,
		" worker-complete ":              true,
		"worker-progress":                false,
		"worker-blocked":                 false,
		"dependency-ready":               false,
		"worker-integration-ready-later": false,
		"worker-ready-later":             false,
		"worker-completed":               false,
	}
	for eventType, want := range tests {
		t.Run(eventType, func(t *testing.T) {
			if got := daemonWorkerIntegrationReadyMailType(eventType); got != want {
				t.Fatalf("daemonWorkerIntegrationReadyMailType(%q) = %v, want %v", eventType, got, want)
			}
		})
	}
}

func TestTaskIntegrationReadinessRequiresCompleteWorkerEvidencePacket(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-worker-evidence-ready"
	repoDir := t.TempDir()
	issuesClient := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "parent", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "child", Type: domain.TypeTask, Status: domain.StatusInReview, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := appendMailboxEvent(repoDir, daemonMailEvent{
		Seq:         1,
		ParentIssue: parentID,
		IssueID:     childID,
		Type:        "worker-integration-ready",
		Body: `{
			"schema": "worker_evidence.v1",
			"summary": "Ready for integration.",
			"commands_run": ["go test ./internal/daemon"],
			"key_assertions": ["integration readiness accepts complete worker evidence"],
			"files_changed": ["internal/daemon/task_commands.go"],
			"review": {"status": "clean", "findings": []},
			"risks": ["none"]
		}`,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("append mailbox event: %v", err)
	}
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 1},
	}

	result, err := d.taskIntegrationReadiness(ctx, projectID, childID, repoDir)
	if err != nil {
		t.Fatalf("taskIntegrationReadiness error: %v", err)
	}
	if !result.Ready || result.EvidenceEventSeq != 1 || result.EvidencePacket == nil {
		t.Fatalf("result = %+v, want ready with evidence packet", result)
	}
	if result.EvidencePacket.Schema != domain.WorkerEvidenceSchemaV1 {
		t.Fatalf("evidence packet = %+v", result.EvidencePacket)
	}
}

func TestTaskIntegrationReadinessReportsIncompleteWorkerEvidencePacket(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-worker-evidence-incomplete"
	repoDir := t.TempDir()
	issuesClient := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "parent", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "child", Type: domain.TypeTask, Status: domain.StatusInReview, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := appendMailboxEvent(repoDir, daemonMailEvent{
		Seq:         1,
		ParentIssue: parentID,
		IssueID:     childID,
		Type:        "worker-integration-ready",
		Body:        `{"schema":"worker_evidence.v1","summary":"Ready","review":{"status":"clean","findings":[]}}`,
		CreatedAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("append mailbox event: %v", err)
	}
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 1},
	}

	result, err := d.taskIntegrationReadiness(ctx, projectID, childID, repoDir)
	if err != nil {
		t.Fatalf("taskIntegrationReadiness error: %v", err)
	}
	if result.Ready || !result.EvidenceIncomplete || result.EvidenceEventSeq != 1 {
		t.Fatalf("result = %+v, want incomplete evidence", result)
	}
	reasons := strings.Join(result.Reasons, "\n")
	for _, want := range []string{"worker evidence packet in mailbox event seq 1 is incomplete", "missing commands_run", "missing files_changed", "missing key_assertions", "missing risks"} {
		if !strings.Contains(reasons, want) {
			t.Fatalf("reasons = %+v, missing %q", result.Reasons, want)
		}
	}
}

func TestTaskIntegrationReadinessLatestWorkerEvidenceEventWins(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-worker-evidence-latest"
	repoDir := t.TempDir()
	issuesClient := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "parent", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "child", Type: domain.TypeTask, Status: domain.StatusInReview, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	events := []daemonMailEvent{
		{
			Seq:         1,
			ParentIssue: parentID,
			IssueID:     childID,
			Type:        "worker-integration-ready",
			Body: `{
				"schema": "worker_evidence.v1",
				"summary": "Earlier complete packet.",
				"commands_run": ["go test ./internal/daemon"],
				"key_assertions": ["older complete evidence should not hide latest incomplete evidence"],
				"files_changed": ["internal/daemon/task_commands.go"],
				"review": {"status": "clean", "findings": []},
				"risks": ["none"]
			}`,
			CreatedAt: time.Now().UTC(),
		},
		{
			Seq:         2,
			ParentIssue: parentID,
			IssueID:     childID,
			Type:        "worker-integration-ready",
			Body:        `{"schema":"worker_evidence.v1","summary":"Latest packet is incomplete","review":{"status":"clean","findings":[]}}`,
			CreatedAt:   time.Now().UTC(),
		},
	}
	for _, event := range events {
		if err := appendMailboxEvent(repoDir, event); err != nil {
			t.Fatalf("append mailbox event seq %d: %v", event.Seq, err)
		}
	}
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 1},
	}

	result, err := d.taskIntegrationReadiness(ctx, projectID, childID, repoDir)
	if err != nil {
		t.Fatalf("taskIntegrationReadiness error: %v", err)
	}
	if result.Ready || !result.EvidenceIncomplete || result.EvidenceEventSeq != 2 {
		t.Fatalf("result = %+v, want latest incomplete evidence at seq 2", result)
	}
}

func TestTaskIntegrationReadinessAcceptsLegacyAliasOnlyWithStructuredEvidence(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-worker-evidence-legacy-alias"
	repoDir := t.TempDir()
	issuesClient := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "parent", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "child", Type: domain.TypeTask, Status: domain.StatusInReview, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := appendMailboxEvent(repoDir, daemonMailEvent{
		Seq:         1,
		ParentIssue: parentID,
		IssueID:     childID,
		Type:        "worker-complete",
		Body: `{
			"schema": "worker_evidence.v1",
			"summary": "Ready through a legacy alias.",
			"commands_run": ["go test ./internal/daemon"],
			"key_assertions": ["legacy aliases still require structured evidence"],
			"files_changed": ["internal/daemon/task_commands.go"],
			"review": {"status": "clean", "findings": []},
			"risks": ["none"]
		}`,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("append mailbox event: %v", err)
	}
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 1},
	}

	result, err := d.taskIntegrationReadiness(ctx, projectID, childID, repoDir)
	if err != nil {
		t.Fatalf("taskIntegrationReadiness error: %v", err)
	}
	if !result.Ready || result.EvidencePacket == nil {
		t.Fatalf("result = %+v, want ready with structured legacy alias evidence", result)
	}
}

func TestTaskMergeBaseTargetSelectsNearestAncestorWorktree(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-merge-base-target"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "parent",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "child",
		Type:     domain.TypeTask,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}

	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	parentWorktree := filepath.Join(repoDir, "wt-parent")
	childWorktree := filepath.Join(repoDir, "wt-child")
	parentBranch := "riordan/" + parentID + "/parent"
	childBranch := "riordan/" + childID + "/child"
	for _, row := range []daemonstate.WorktreeState{
		{ProjectID: projectID, IssueID: parentID, Path: parentWorktree, Branch: parentBranch, UpdatedAt: time.Now().UTC()},
		{ProjectID: projectID, IssueID: childID, Path: childWorktree, Branch: childBranch, UpdatedAt: time.Now().UTC()},
	} {
		if err := store.UpsertWorktreeState(ctx, row); err != nil {
			t.Fatalf("seed worktree state: %v", err)
		}
	}

	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain" {
			return strings.Join([]string{
				"worktree " + repoDir,
				"branch refs/heads/main",
				"",
				"worktree " + parentWorktree,
				"branch refs/heads/" + parentBranch,
				"",
				"worktree " + childWorktree,
				"branch refs/heads/" + childBranch,
				"",
			}, "\n"), nil
		}
		t.Fatalf("unexpected git args: %v", args)
		return "", nil
	}}
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(projectID string) *git.WorktreeManager { return d.worktreeManagerForProject(projectID) },
		runtimeStateStore: store,
		logger:            slog.Default(),
		pollers:           map[string]context.CancelFunc{normalizedProjectID(projectID): func() {}},
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})

	got, err := d.taskMergeBaseTarget(ctx, projectID, childID, "main", false)
	if err != nil {
		t.Fatalf("taskMergeBaseTarget error: %v", err)
	}
	if got.TargetID != parentID || got.Branch != parentBranch || got.WorktreePath != parentWorktree || !got.BranchAttached {
		t.Fatalf("merge target = %+v, want parent %s branch %s worktree %s", got, parentID, parentBranch, parentWorktree)
	}
}

func TestTaskMergeBaseTargetDefaultUsesProjectBaseBranch(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	configDir := filepath.Join(repoDir, ".azedarach")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configJSON := `{
		"$version": 8,
		"git": {
			"baseBranch": "main"
		}
	}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configJSON), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	d := New(Config{
		RepoDir:    repoDir,
		BaseBranch: "preview",
		Logger:     slog.Default(),
	})
	got, err := d.taskMergeBaseTarget(ctx, filepath.Base(repoDir), "", "", false)
	if err != nil {
		t.Fatalf("taskMergeBaseTarget error: %v", err)
	}
	if got.Branch != "main" {
		t.Fatalf("Branch = %q, want project base branch main", got.Branch)
	}
}

func TestTaskGraphReadinessReportsPendingStartupAndCleanupTranscriptStates(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-graph-transcript"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := issues.NewClient(repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Root",
		Type:   domain.TypeEpic,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	pendingID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Pending child",
		Type:     domain.TypeTask,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create pending child: %v", err)
	}
	startingID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Starting child",
		Type:     domain.TypeTask,
		Status:   domain.StatusInProgress,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create starting child: %v", err)
	}
	closedID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Closed child",
		Type:     domain.TypeTask,
		Status:   domain.StatusDone,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create closed child: %v", err)
	}

	runtime := newOperationRuntime(operationRuntimeConfig{
		repoDir: repoDir,
		logger:  logger,
		hub:     publish.NewHub(16, 8, logger),
	})
	t.Cleanup(func() { _ = runtime.Close() })
	submittedAt := time.Date(2026, time.June, 17, 8, 0, 0, 0, time.UTC)
	if _, err := runtime.store.Create(ctx, opstore.CreateParams{
		OperationID:  "op-session-start",
		ProjectID:    projectID,
		IssueID:      pendingID,
		Kind:         daemonhandlers.CommandSessionStart,
		DedupeKey:    "session.start:" + pendingID,
		ResourceKeys: []string{"issue:" + projectID + ":" + pendingID},
		State:        opstore.StateQueued,
		SubmittedAt:  submittedAt,
	}); err != nil {
		t.Fatalf("seed pending session.start operation: %v", err)
	}

	sessionStore := daemonstate.NewStore()
	startingSessionID := naming.CanonicalSessionID(projectID, startingID)
	if _, err := sessionStore.UpsertSession(projectID, startingSessionID, startingID, daemonstate.SessionStateStarting); err != nil {
		t.Fatalf("seed starting session: %v", err)
	}
	closedSessionID := naming.CanonicalSessionID(projectID, closedID)
	if _, err := sessionStore.UpsertSession(projectID, closedSessionID, closedID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed stale closed session: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		sessionStore:     sessionStore,
		operationRuntime: runtime,
		revision:         map[string]uint64{projectID: 1},
	}

	ready, err := d.taskGraphReadiness(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("taskGraphReadiness error: %v", err)
	}
	if slices.Contains(ready.Runnable, pendingID) {
		t.Fatalf("pending child still runnable: %+v", ready)
	}
	if len(ready.Pending) != 1 || ready.Pending[0].IssueID != pendingID || ready.Pending[0].OperationID != "op-session-start" || ready.Pending[0].OperationState != string(opstore.StateQueued) {
		t.Fatalf("pending = %+v", ready.Pending)
	}
	if len(ready.Active) != 1 || ready.Active[0] != startingID {
		t.Fatalf("active = %+v, want starting child only", ready.Active)
	}
	byID := map[string]taskGraphActiveSession{}
	for _, active := range ready.ActiveSessions {
		byID[active.IssueID] = active
	}
	if got := byID[startingID]; got.Status != "active" || got.Activity != "starting" || got.ActivitySource != "startup-grace" || got.Advice != "" {
		t.Fatalf("starting active session = %+v", got)
	}
	if got := byID[closedID]; got.Status != "cleanup-pending" || !strings.Contains(got.Advice, "cleanup is pending") {
		t.Fatalf("closed active session = %+v", got)
	}
}

func TestTaskGraphPendingSessionStartsCanonicalizesProjectID(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	canonicalProjectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}

	runtime := newOperationRuntime(operationRuntimeConfig{
		repoDir: repoDir,
		logger:  logger,
		hub:     publish.NewHub(16, 8, logger),
	})
	t.Cleanup(func() { _ = runtime.Close() })
	if _, err := runtime.store.Create(ctx, opstore.CreateParams{
		OperationID:  "op-session-start",
		ProjectID:    canonicalProjectID,
		IssueID:      "az-2",
		Kind:         daemonhandlers.CommandSessionStart,
		DedupeKey:    "session.start:az-2",
		ResourceKeys: []string{"issue:" + canonicalProjectID + ":az-2"},
		State:        opstore.StateQueued,
		SubmittedAt:  time.Date(2026, time.June, 17, 8, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed pending session.start operation: %v", err)
	}
	d := &Daemon{
		cfg:              Config{RepoDir: repoDir, Logger: logger},
		operationRuntime: runtime,
	}

	pending, err := d.taskGraphPendingSessionStarts(ctx, protocol.DefaultProjectID)
	if err != nil {
		t.Fatalf("taskGraphPendingSessionStarts error: %v", err)
	}
	got, ok := pending["az-2"]
	if !ok || got.OperationID != "op-session-start" || got.OperationState != string(opstore.StateQueued) {
		t.Fatalf("pending = %+v", pending)
	}
}

func TestTaskFollowOnMergeCandidatesAreDaemonOwned(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-follow-on-candidates"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Parent epic", Type: domain.TypeEpic})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if err := issuesClient.Update(ctx, parentID, domain.StatusInProgress); err != nil {
		t.Fatalf("update parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Child task", Type: domain.TypeTask, ParentID: &parentID})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := issuesClient.Update(ctx, childID, domain.StatusInProgress); err != nil {
		t.Fatalf("update child: %v", err)
	}
	blockerID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Ready blocker", Type: domain.TypeTask})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	if err := issuesClient.Update(ctx, blockerID, domain.StatusInProgress); err != nil {
		t.Fatalf("update blocker: %v", err)
	}
	openID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Open blocker", Type: domain.TypeTask})
	if err != nil {
		t.Fatalf("create open blocker: %v", err)
	}
	if err := issuesClient.AddDependency(ctx, childID, blockerID, string(domain.DependencyBlocks)); err != nil {
		t.Fatalf("add ready blocker dependency: %v", err)
	}
	if err := issuesClient.AddDependency(ctx, childID, openID, string(domain.DependencyBlocks)); err != nil {
		t.Fatalf("add open blocker dependency: %v", err)
	}

	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	for _, row := range []daemonstate.WorktreeState{
		{ProjectID: projectID, IssueID: parentID, Path: filepath.Join(repoDir, "wt-parent"), Branch: "az/" + parentID, UpdatedAt: time.Now().UTC()},
		{ProjectID: projectID, IssueID: blockerID, Path: filepath.Join(repoDir, "wt-blocker"), Branch: "az/" + blockerID, UpdatedAt: time.Now().UTC()},
		{ProjectID: projectID, IssueID: openID, Path: filepath.Join(repoDir, "wt-open"), Branch: "az/" + openID, UpdatedAt: time.Now().UTC()},
	} {
		if err := store.UpsertWorktreeState(ctx, row); err != nil {
			t.Fatalf("seed worktree state: %v", err)
		}
	}

	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
	}

	got, err := d.taskFollowOnMergeCandidates(ctx, projectID, childID)
	if err != nil {
		t.Fatalf("taskFollowOnMergeCandidates error: %v", err)
	}
	if got.MergeTargetToBase {
		t.Fatal("MergeTargetToBase = true, want false for child")
	}
	if len(got.Candidates) != 2 {
		t.Fatalf("candidate count = %d, want 2: %+v", len(got.Candidates), got.Candidates)
	}
	if got.Candidates[0].IssueID != parentID || got.Candidates[0].Relation != string(domain.DependencyParentChild) {
		t.Fatalf("first candidate = %+v, want parent", got.Candidates[0])
	}
	if got.Candidates[1].IssueID != blockerID || got.Candidates[1].Relation != string(domain.DependencyBlocks) {
		t.Fatalf("second candidate = %+v, want ready blocker", got.Candidates[1])
	}
	for _, candidate := range got.Candidates {
		if candidate.IssueID == openID {
			t.Fatalf("open blocker should not be eligible: %+v", got.Candidates)
		}
		if !candidate.HasWorktree {
			t.Fatalf("candidate missing worktree signal: %+v", candidate)
		}
	}
}

func TestTaskFollowOnMergeCandidatesAllowsTopLevelBaseFallback(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-follow-on-top"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	topID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Top task", Type: domain.TypeTask})
	if err != nil {
		t.Fatalf("create top task: %v", err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
	}

	got, err := d.taskFollowOnMergeCandidates(ctx, projectID, topID)
	if err != nil {
		t.Fatalf("taskFollowOnMergeCandidates error: %v", err)
	}
	if !got.MergeTargetToBase {
		t.Fatal("MergeTargetToBase = false, want true for top-level task")
	}
	if len(got.Candidates) != 0 {
		t.Fatalf("candidates = %+v, want none", got.Candidates)
	}
}

func TestTaskDeleteRuntimeBlockersAreDaemonOwned(t *testing.T) {
	task := domain.Task{
		ID:             naming.IssueID("az-1"),
		HasTmuxSession: true,
		HasWorktree:    true,
	}
	blockers := daemonTaskDeleteRuntimeBlockers(task)
	if strings.Join(blockers, ",") != "session,worktree" {
		t.Fatalf("blockers = %v, want session/worktree", blockers)
	}
}

func TestTaskDeleteCommandDeletesThroughDaemon(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-task-delete"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Delete me",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		hub: publish.NewHub(16, 8, slog.Default()),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
		revision: map[string]uint64{},
	}
	body, err := json.Marshal(taskDeleteRequest{TaskID: taskID})
	if err != nil {
		t.Fatalf("marshal delete request: %v", err)
	}
	resp, err := d.handleTaskDelete(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-delete",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.delete",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskDelete error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("handleTaskDelete response = %+v", resp)
	}
	var result taskDeleteResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal delete response: %v", err)
	}
	if result.TaskID != taskID || !result.Deleted || result.Revision == 0 {
		t.Fatalf("delete result = %+v", result)
	}
	tasks, err := issuesClient.List(ctx)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	for _, task := range tasks {
		if task.ID.String() == taskID {
			t.Fatalf("task %s still present after delete", taskID)
		}
	}
}

func TestTaskDeleteCommandRejectsParentWithUndeletedChildren(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-task-delete-parent-children"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Parent",
		Type:  domain.TypeEpic,
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Child",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := issuesClient.AddDependency(ctx, childID, parentID, "parent-child"); err != nil {
		t.Fatalf("add parent-child: %v", err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		hub: publish.NewHub(16, 8, slog.Default()),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
		revision: map[string]uint64{},
	}
	body, err := json.Marshal(taskDeleteRequest{TaskID: parentID})
	if err != nil {
		t.Fatalf("marshal delete request: %v", err)
	}
	resp, err := d.handleTaskDelete(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-delete-parent-children",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.delete",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskDelete error: %v", err)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("handleTaskDelete response = %+v, want guard error", resp)
	}
	if resp.Error.Code != protocol.ErrorCodeConflict {
		t.Fatalf("error code = %s, want %s", resp.Error.Code, protocol.ErrorCodeConflict)
	}
	if !strings.Contains(resp.Error.Message, "cannot delete issue "+parentID) || !strings.Contains(resp.Error.Message, "1 undeleted descendant") {
		t.Fatalf("error message = %q", resp.Error.Message)
	}
	tasks, err := issuesClient.Search(ctx, parentID)
	if err != nil {
		t.Fatalf("search parent: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("parent search len = %d, want 1", len(tasks))
	}
}

func TestTaskArchiveCommandRejectsParentWithUndeletedChildren(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-task-archive-parent-children"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Parent",
		Type:  domain.TypeEpic,
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Child",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := issuesClient.AddDependency(ctx, childID, parentID, "parent-child"); err != nil {
		t.Fatalf("add parent-child: %v", err)
	}
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		hub: publish.NewHub(16, 8, slog.Default()),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{},
	}
	body, err := json.Marshal(struct {
		TaskID string `json:"task_id"`
	}{TaskID: parentID})
	if err != nil {
		t.Fatalf("marshal archive request: %v", err)
	}
	resp, err := d.handleTaskArchive(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-archive-parent-children",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.archive",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskArchive error: %v", err)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("handleTaskArchive response = %+v, want guard error", resp)
	}
	if resp.Error.Code != protocol.ErrorCodeConflict {
		t.Fatalf("error code = %s, want %s", resp.Error.Code, protocol.ErrorCodeConflict)
	}
	if !strings.Contains(resp.Error.Message, "cannot archive issue "+parentID) || !strings.Contains(resp.Error.Message, "1 undeleted descendant") {
		t.Fatalf("error message = %q", resp.Error.Message)
	}
	tasks, err := issuesClient.Search(ctx, parentID)
	if err != nil {
		t.Fatalf("search parent: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("parent search len = %d, want 1", len(tasks))
	}
}

func TestTaskDeleteCommandRejectsParentWithChildrenBeforeRuntimeCleanup(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-task-delete-parent-children-runtime"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Parent with runtime",
		Type:  domain.TypeEpic,
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Child",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := issuesClient.AddDependency(ctx, childID, parentID, "parent-child"); err != nil {
		t.Fatalf("add parent-child: %v", err)
	}
	worktreePath := filepath.Join(repoDir, "wt-"+parentID)
	branchName := "riordan/" + parentID + "/parent-runtime"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   parentID,
		Path:      worktreePath,
		Branch:    branchName,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	cleanupMarker := filepath.Join(repoDir, "delete-parent-cleanup-marker")
	worktreeDeleteCalled := false
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case joined == "worktree list --porcelain":
			return fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", worktreePath, branchName), nil
		case strings.Contains(joined, "status --porcelain"):
			return "", nil
		case strings.HasPrefix(joined, "worktree remove "):
			worktreeDeleteCalled = true
			return "", nil
		default:
			return "", nil
		}
	}}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			SessionShell: "sh",
			BaseBranch:   "main",
			IssueResources: appconfig.IssueResourcesConfig{
				CleanupCommands: []string{
					fmt.Sprintf("touch %q", cleanupMarker),
				},
			},
			Logger: logger,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}

	body, err := json.Marshal(taskDeleteRequest{
		TaskID:         parentID,
		Cleanup:        true,
		RemoveWorktree: true,
		ForceWorktree:  true,
	})
	if err != nil {
		t.Fatalf("marshal delete request: %v", err)
	}
	resp, err := d.handleTaskDelete(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-delete-parent-runtime-children",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.delete",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskDelete error: %v", err)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("handleTaskDelete response = %+v, want guard error", resp)
	}
	if resp.Error.Code != protocol.ErrorCodeConflict {
		t.Fatalf("error code = %s, want %s", resp.Error.Code, protocol.ErrorCodeConflict)
	}
	if _, err := os.Stat(cleanupMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleanup marker stat err = %v, want not exist", err)
	}
	if worktreeDeleteCalled {
		t.Fatal("worktree delete called before live-child guard")
	}
}

func TestTaskDeleteRunsIssueResourceCleanupWithoutSessionBeforeWorktreeRemoval(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-task-delete-resource-cleanup"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Delete with resource cleanup",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	worktreePath := filepath.Join(repoDir, "wt-"+taskID)
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	branchName := "riordan/" + taskID + "/resource-cleanup"
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      worktreePath,
		Branch:    branchName,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	cleanupMarker := filepath.Join(repoDir, "delete-cleanup-marker")
	commands := make([]string, 0, 8)
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		joined := strings.Join(args, " ")
		commands = append(commands, joined)
		switch {
		case joined == "worktree list --porcelain":
			return fmt.Sprintf("worktree %s\nbranch refs/heads/%s\n\n", worktreePath, branchName), nil
		case strings.Contains(joined, "status --porcelain"):
			return "", nil
		case strings.HasPrefix(joined, "worktree remove "):
			if _, err := os.Stat(cleanupMarker); err != nil {
				return "", fmt.Errorf("cleanup marker missing before remove: %w", err)
			}
			return "", nil
		default:
			return "", nil
		}
	}}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			SessionShell: "sh",
			BaseBranch:   "main",
			IssueResources: appconfig.IssueResourcesConfig{
				CleanupCommands: []string{
					fmt.Sprintf("printf '%%s|%%s|%%s' \"$AZEDARACH_ISSUE_ID\" \"$AZEDARACH_WORKTREE_PATH\" \"$AZEDARACH_BRANCH\" > %q", cleanupMarker),
				},
			},
			Logger: logger,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}

	body, err := json.Marshal(taskDeleteRequest{TaskID: taskID, RemoveWorktree: true})
	if err != nil {
		t.Fatalf("marshal delete request: %v", err)
	}
	resp, err := d.handleTaskDelete(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-delete-resource-cleanup",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.delete",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskDelete error: %v", err)
	}
	if !resp.OK {
		if resp.Error != nil {
			t.Fatalf("handleTaskDelete error = %s", resp.Error.Message)
		}
		t.Fatalf("handleTaskDelete response = %+v", resp)
	}
	data, err := os.ReadFile(cleanupMarker)
	if err != nil {
		t.Fatalf("read cleanup marker: %v", err)
	}
	want := taskID + "|" + worktreePath + "|" + branchName
	if strings.TrimSpace(string(data)) != want {
		t.Fatalf("cleanup marker = %q, want %q", strings.TrimSpace(string(data)), want)
	}
	tasks, err := issuesClient.List(ctx)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	for _, task := range tasks {
		if task.ID.String() == taskID {
			t.Fatalf("task %s still present after delete", taskID)
		}
	}
	if !strings.Contains(strings.Join(commands, "\n"), "worktree remove "+worktreePath) {
		t.Fatalf("commands = %v, want worktree remove", commands)
	}
}

func TestTaskDeleteCleanupRepairsStaleMissingWorktreeProjection(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-task-delete-stale-runtime"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Delete stale runtime",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := issuesClient.UpdateWithRuntime(ctx, projectID, taskID, domain.StatusInProgress); err != nil {
		t.Fatalf("mark task in progress: %v", err)
	}
	sessionID := naming.CanonicalSessionID(projectID, taskID)
	if err := runtimeStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   taskID,
		State:     daemonstate.SessionStateAttached,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed session projection: %v", err)
	}
	staleWorktreePath := filepath.Join(repoDir, "missing-"+taskID)
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   taskID,
		Path:      staleWorktreePath,
		Branch:    "riordan/" + taskID + "/stale-runtime",
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if joined == "worktree list --porcelain" {
			return fmt.Sprintf("worktree %s\nbranch refs/heads/main\n\n", repoDir), nil
		}
		return "", nil
	}}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			SessionShell: "sh",
			BaseBranch:   "main",
			Logger:       logger,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: manager,
		},
		sessionStore: daemonstate.NewStore(),
		revision:     map[string]uint64{projectID: 1},
		hub:          publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}
	body, err := json.Marshal(taskDeleteRequest{
		TaskID:         taskID,
		Cleanup:        true,
		StopSession:    true,
		RemoveWorktree: true,
		ForceWorktree:  true,
	})
	if err != nil {
		t.Fatalf("marshal delete request: %v", err)
	}
	resp, err := d.handleTaskDelete(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-delete-stale-runtime",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.delete",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskDelete error: %v", err)
	}
	if !resp.OK {
		if resp.Error != nil {
			t.Fatalf("handleTaskDelete error = %s", resp.Error.Message)
		}
		t.Fatalf("handleTaskDelete response = %+v", resp)
	}

	tasks, err := issuesClient.Search(ctx, taskID)
	if err != nil {
		t.Fatalf("search task: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("task %s still present after cleanup delete", taskID)
	}
	if _, ok, err := runtimeStore.GetWorktreeStateByIssueID(ctx, projectID, taskID); err != nil {
		t.Fatalf("get worktree projection: %v", err)
	} else if ok {
		t.Fatalf("stale worktree projection still present for %s", taskID)
	}
	session, ok, err := runtimeStore.GetSessionStateByIssueID(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get session projection: %v", err)
	}
	if !ok {
		t.Fatalf("session projection missing for %s, want stopped repair marker", taskID)
	}
	if session.State != daemonstate.SessionStateStopped || session.ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("session projection = %+v, want desired/observed stopped", session)
	}
}

func TestTaskDeleteCleanupRepairsStaleLegacyProjectWorktreeProjection(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-task-delete-current-runtime"
	legacyProjectID := "proj-task-delete-legacy-runtime"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Delete stale legacy runtime",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	staleWorktreePath := filepath.Join(repoDir, "missing-legacy-"+taskID)
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: legacyProjectID,
		IssueID:   taskID,
		Path:      staleWorktreePath,
		Branch:    "riordan/" + taskID + "/legacy-stale-runtime",
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed legacy worktree projection: %v", err)
	}

	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if joined == "worktree list --porcelain" {
			return fmt.Sprintf("worktree %s\nbranch refs/heads/main\n\n", repoDir), nil
		}
		return "", nil
	}}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			SessionShell: "sh",
			BaseBranch:   "main",
			Logger:       logger,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: manager,
		},
		sessionStore: daemonstate.NewStore(),
		revision:     map[string]uint64{projectID: 1},
		hub:          publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}
	body, err := json.Marshal(taskDeleteRequest{
		TaskID:         taskID,
		Cleanup:        true,
		RemoveWorktree: true,
		ForceWorktree:  true,
	})
	if err != nil {
		t.Fatalf("marshal delete request: %v", err)
	}
	resp, err := d.handleTaskDelete(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-delete-stale-legacy-runtime",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.delete",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskDelete error: %v", err)
	}
	if !resp.OK {
		if resp.Error != nil {
			t.Fatalf("handleTaskDelete error = %s", resp.Error.Message)
		}
		t.Fatalf("handleTaskDelete response = %+v", resp)
	}

	tasks, err := issuesClient.Search(ctx, taskID)
	if err != nil {
		t.Fatalf("search task: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("task %s still present after cleanup delete", taskID)
	}
	if _, ok, err := runtimeStore.GetWorktreeStateByIssueID(ctx, legacyProjectID, taskID); err != nil {
		t.Fatalf("get legacy worktree projection: %v", err)
	} else if ok {
		t.Fatalf("stale legacy worktree projection still present for %s", taskID)
	}
}

func TestTaskDeleteCleanupRepairsLegacyProjectWorktreeProjectionMissingFromGitList(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-task-delete-current-git-list"
	legacyProjectID := "proj-task-delete-legacy-git-list"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Delete stale legacy runtime absent from git list",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	staleWorktreePath := filepath.Join(repoDir, "existing-but-unlisted-"+taskID)
	if err := os.MkdirAll(staleWorktreePath, 0o755); err != nil {
		t.Fatalf("mkdir stale worktree path: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: legacyProjectID,
		IssueID:   taskID,
		Path:      staleWorktreePath,
		Branch:    "riordan/" + taskID + "/legacy-unlisted-runtime",
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed legacy worktree projection: %v", err)
	}

	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if joined == "worktree list --porcelain" {
			return fmt.Sprintf("worktree %s\nbranch refs/heads/main\n\n", repoDir), nil
		}
		return "", nil
	}}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			SessionShell: "sh",
			BaseBranch:   "main",
			Logger:       logger,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: manager,
		},
		sessionStore: daemonstate.NewStore(),
		revision:     map[string]uint64{projectID: 1},
		hub:          publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}
	body, err := json.Marshal(taskDeleteRequest{
		TaskID:         taskID,
		Cleanup:        true,
		RemoveWorktree: true,
		ForceWorktree:  true,
	})
	if err != nil {
		t.Fatalf("marshal delete request: %v", err)
	}
	resp, err := d.handleTaskDelete(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-delete-stale-legacy-runtime-git-list",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.delete",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskDelete error: %v", err)
	}
	if !resp.OK {
		if resp.Error != nil {
			t.Fatalf("handleTaskDelete error = %s", resp.Error.Message)
		}
		t.Fatalf("handleTaskDelete response = %+v", resp)
	}

	tasks, err := issuesClient.Search(ctx, taskID)
	if err != nil {
		t.Fatalf("search task: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("task %s still present after cleanup delete", taskID)
	}
	if _, ok, err := runtimeStore.GetWorktreeStateByIssueID(ctx, legacyProjectID, taskID); err != nil {
		t.Fatalf("get legacy worktree projection: %v", err)
	} else if ok {
		t.Fatalf("git-unlisted legacy worktree projection still present for %s", taskID)
	}
}

func TestTaskDeleteCleanupBlocksLiveLegacyProjectWorktreeProjectionFromGitList(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-task-delete-current-live-git-list"
	legacyProjectID := "proj-task-delete-legacy-live-git-list"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Delete live legacy runtime still in git list",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	liveWorktreePath := filepath.Join(repoDir, "existing-live-"+taskID)
	if err := os.MkdirAll(liveWorktreePath, 0o755); err != nil {
		t.Fatalf("mkdir live worktree path: %v", err)
	}
	if err := runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: legacyProjectID,
		IssueID:   taskID,
		Path:      liveWorktreePath,
		Branch:    "feature/unparseable",
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed legacy worktree projection: %v", err)
	}

	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if joined == "worktree list --porcelain" {
			return fmt.Sprintf("worktree %s\nbranch refs/heads/main\n\nworktree %s\nbranch refs/heads/feature/unparseable\n\n", repoDir, liveWorktreePath), nil
		}
		return "", nil
	}}
	manager := git.NewWorktreeManager(runner, repoDir, logger)
	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			SessionShell: "sh",
			BaseBranch:   "main",
			Logger:       logger,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: manager,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: manager,
		},
		sessionStore: daemonstate.NewStore(),
		revision:     map[string]uint64{projectID: 1},
		hub:          publish.NewHub(16, 8, logger),
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject: func(string) *git.WorktreeManager { return manager },
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return runtimeStore
		},
		runtimeProjectionWriter: d.runtimeProjectionStateWriter(),
		logger:                  logger,
	}

	body, err := json.Marshal(taskDeleteRequest{
		TaskID:         taskID,
		Cleanup:        true,
		RemoveWorktree: true,
		ForceWorktree:  true,
	})
	if err != nil {
		t.Fatalf("marshal delete request: %v", err)
	}
	resp, err := d.handleTaskDelete(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-delete-live-legacy-runtime-git-list",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.delete",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskDelete error: %v", err)
	}
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "active runtime projection aliases remain") {
		t.Fatalf("handleTaskDelete response = %+v, want live alias blocker", resp)
	}
	if _, ok, err := runtimeStore.GetWorktreeStateByIssueID(ctx, legacyProjectID, taskID); err != nil {
		t.Fatalf("get legacy worktree projection: %v", err)
	} else if !ok {
		t.Fatalf("live legacy worktree projection was removed for %s", taskID)
	}
}

func TestTaskDeleteRunsIssueResourceCleanupWithoutRuntimeAttachments(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-task-delete-resource-cleanup-root"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Delete root cleanup",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	cleanupMarker := filepath.Join(repoDir, "delete-root-cleanup-marker")
	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			SessionShell: "sh",
			BaseBranch:   "main",
			IssueResources: appconfig.IssueResourcesConfig{
				CleanupCommands: []string{
					fmt.Sprintf("printf '%%s|%%s|%%s|%%s' \"$AZEDARACH_ISSUE_ID\" \"$AZEDARACH_WORKTREE_PATH\" \"$AZEDARACH_BRANCH\" \"$(pwd)\" > %q", cleanupMarker),
				},
			},
			Logger: logger,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, logger),
	}

	body, err := json.Marshal(taskDeleteRequest{TaskID: taskID})
	if err != nil {
		t.Fatalf("marshal delete request: %v", err)
	}
	resp, err := d.handleTaskDelete(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-delete-root-resource-cleanup",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.delete",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskDelete error: %v", err)
	}
	if !resp.OK {
		if resp.Error != nil {
			t.Fatalf("handleTaskDelete error = %s", resp.Error.Message)
		}
		t.Fatalf("handleTaskDelete response = %+v", resp)
	}
	data, err := os.ReadFile(cleanupMarker)
	if err != nil {
		t.Fatalf("read cleanup marker: %v", err)
	}
	wantRoot, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		t.Fatalf("eval root symlink: %v", err)
	}
	want := taskID + "|||" + wantRoot
	if strings.TrimSpace(string(data)) != want {
		t.Fatalf("cleanup marker = %q, want %q", strings.TrimSpace(string(data)), want)
	}
	tasks, err := issuesClient.List(ctx)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	for _, task := range tasks {
		if task.ID.String() == taskID {
			t.Fatalf("task %s still present after delete", taskID)
		}
	}
}

func TestTaskUpdateStatusRejectsInReviewWithUnreadyChildren(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-review-guard-children"
	d, issuesClient := newTaskStatusReviewGuardDaemon(t, projectID)

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Parent", Type: domain.TypeTask})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Child",
		Type:     domain.TypeTask,
		Status:   domain.StatusInProgress,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}

	resp := updateStatusForTest(t, d, projectID, parentID, domain.StatusInReview, false)
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "child issues are not review-ready") || !strings.Contains(resp.Error.Message, childID+" (in_progress)") || !strings.Contains(resp.Error.Message, "--cascade-children") {
		t.Fatalf("task.update_status response = %+v, want child readiness guard", resp)
	}
	parent, err := issuesClient.GetWithRuntime(ctx, projectID, parentID)
	if err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if parent.Status != domain.StatusOpen {
		t.Fatalf("parent status = %s, want open", parent.Status)
	}
}

func TestTaskUpdateStatusCascadeChildrenMovesNestedDescendantsToInReview(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-review-cascade-children"
	d, issuesClient := newTaskStatusReviewGuardDaemon(t, projectID)

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Parent", Type: domain.TypeTask})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Child",
		Type:     domain.TypeTask,
		Status:   domain.StatusInProgress,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	grandchildID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Grandchild",
		Type:     domain.TypeTask,
		Status:   domain.StatusOpen,
		ParentID: &childID,
	})
	if err != nil {
		t.Fatalf("create grandchild: %v", err)
	}

	resp := updateStatusForTest(t, d, projectID, parentID, domain.StatusInReview, true)
	if !resp.OK || resp.Error != nil {
		t.Fatalf("task.update_status response = %+v, want cascade success", resp)
	}
	for _, id := range []string{parentID, childID, grandchildID} {
		task, err := issuesClient.GetWithRuntime(ctx, projectID, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if task.Status != domain.StatusInReview {
			t.Fatalf("%s status = %s, want in_review", id, task.Status)
		}
	}
}

func TestTaskUpdateStatusReviewGuardHonorsLegacyParentChildDependency(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-review-guard-legacy-edge"
	d, issuesClient := newTaskStatusReviewGuardDaemon(t, projectID)

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Parent", Type: domain.TypeTask})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Child", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := issuesClient.AddDependency(ctx, childID, parentID, string(domain.DependencyParentChild)); err != nil {
		t.Fatalf("add parent-child: %v", err)
	}

	resp := updateStatusForTest(t, d, projectID, parentID, domain.StatusInReview, false)
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, childID+" (open)") {
		t.Fatalf("task.update_status response = %+v, want legacy child guard", resp)
	}
}

func TestTaskUpdateStatusAllowsInReviewWhenChildrenReviewReady(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-review-guard-ready"
	d, issuesClient := newTaskStatusReviewGuardDaemon(t, projectID)

	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Parent", Type: domain.TypeTask})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	for _, status := range []domain.Status{domain.StatusInReview, domain.StatusDone} {
		if _, err := issuesClient.Create(ctx, issues.CreateTaskParams{
			Title:    "Child",
			Type:     domain.TypeTask,
			Status:   status,
			ParentID: &parentID,
		}); err != nil {
			t.Fatalf("create child %s: %v", status, err)
		}
	}

	resp := updateStatusForTest(t, d, projectID, parentID, domain.StatusInReview, false)
	if !resp.OK || resp.Error != nil {
		t.Fatalf("task.update_status response = %+v, want success", resp)
	}
	parent, err := issuesClient.GetWithRuntime(ctx, projectID, parentID)
	if err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if parent.Status != domain.StatusInReview {
		t.Fatalf("parent status = %s, want in_review", parent.Status)
	}
}

func TestTaskUpdateStatusRejectsCascadeChildrenForNonReviewStatus(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-review-cascade-non-review"
	d, issuesClient := newTaskStatusReviewGuardDaemon(t, projectID)

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Task", Type: domain.TypeTask})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	resp := updateStatusForTest(t, d, projectID, taskID, domain.StatusInProgress, true)
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "cascade_children is only supported with status in_review") {
		t.Fatalf("task.update_status response = %+v, want cascade non-review rejection", resp)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.Status != domain.StatusOpen {
		t.Fatalf("task status = %s, want open", task.Status)
	}
}

func newTaskStatusReviewGuardDaemon(t *testing.T, projectID string) (*Daemon, *issues.Client) {
	t.Helper()
	repoDir := t.TempDir()
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	return &Daemon{
		cfg: Config{RepoDir: repoDir, Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		revision: map[string]uint64{projectID: 1},
		hub:      publish.NewHub(16, 8, slog.Default()),
	}, issuesClient
}

func updateStatusForTest(t *testing.T, d *Daemon, projectID, taskID string, status domain.Status, cascadeChildren bool) protocol.ResponseEnvelope {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"task_id":          taskID,
		"status":           status,
		"cascade_children": cascadeChildren,
	})
	if err != nil {
		t.Fatalf("marshal status request: %v", err)
	}
	resp, err := d.handleTaskUpdateStatus(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       naming.RequestID("req-review-guard-" + taskID),
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.update_status",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskUpdateStatus error: %v", err)
	}
	return resp
}

func assertNextTaskUpdatedEvent(t *testing.T, events <-chan protocol.EventEnvelope, taskID string, status domain.Status) {
	t.Helper()
	select {
	case evt := <-events:
		if evt.Event != protocol.EventTaskUpdated {
			t.Fatalf("event = %s, want %s", evt.Event, protocol.EventTaskUpdated)
		}
		var body protocol.TaskEventBody
		if err := json.Unmarshal(evt.Body, &body); err != nil {
			t.Fatalf("unmarshal task event body: %v", err)
		}
		if body.TaskID.String() != taskID || body.Task == nil || body.Task.Status != status {
			t.Fatalf("task event body = %+v, want %s -> %s", body, taskID, status)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s event", protocol.EventTaskUpdated)
	}
}

func TestTaskListSnapshotFreshnessMarksStaleProjection(t *testing.T) {
	originalNow := timeNow
	t.Cleanup(func() {
		timeNow = originalNow
	})
	timeNow = func() time.Time {
		return time.Date(2026, time.April, 2, 11, 5, 0, 0, time.UTC)
	}

	ctx := context.Background()
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })

	checkedAt := time.Date(2026, time.April, 2, 11, 4, 0, 0, time.UTC)
	if err := store.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: "proj-stale",
		IssueID:   "az-1",
		Path:      "/tmp/proj-stale-az-1",
		UpdatedAt: checkedAt,
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: slog.Default()},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": store,
		},
	}
	lastCheckedAt, freshness := d.taskListSnapshotFreshness(ctx, "proj-stale")
	if !lastCheckedAt.Equal(checkedAt) {
		t.Fatalf("lastCheckedAt = %v, want %v", lastCheckedAt, checkedAt)
	}
	if freshness != protocol.TaskListFreshnessStale {
		t.Fatalf("freshness = %q, want %q", freshness, protocol.TaskListFreshnessStale)
	}
}

func TestHandleTaskGetManyReturnsBatchDependencyContextWithPartialMiss(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	originalNow := timeNow
	t.Cleanup(func() {
		timeNow = originalNow
	})
	timeNow = func() time.Time {
		return time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC)
	}

	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	projectID := "proj-get-many"
	firstID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "First",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create first issue: %v", err)
	}
	secondID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Second",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create second issue: %v", err)
	}
	if err := issuesClient.AddDependency(ctx, secondID, firstID, string(domain.DependencyBlocks)); err != nil {
		t.Fatalf("add dependency: %v", err)
	}

	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })
	firstSessionID := naming.CanonicalSessionID(projectID, firstID)
	secondSessionID := naming.CanonicalSessionID(projectID, secondID)
	staleUpdatedAt := time.Date(2026, time.April, 2, 10, 0, 0, 0, time.UTC)
	for _, row := range []struct {
		name      string
		sessionID string
		issueID   string
	}{
		{name: "requested", sessionID: secondSessionID, issueID: secondID},
		{name: "context", sessionID: firstSessionID, issueID: firstID},
	} {
		if err := runtimeStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
			ID:            row.sessionID,
			IssueID:       row.issueID,
			State:         daemonstate.SessionStateRunning,
			ObservedState: daemonstate.SessionStateStopped,
			UpdatedAt:     staleUpdatedAt,
		}); err != nil {
			t.Fatalf("seed %s session state: %v", row.name, err)
		}
	}
	tmuxRunner := &testTmuxRunner{
		sessions: map[string]bool{
			firstSessionID:  true,
			secondSessionID: true,
		},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}

	d := &Daemon{
		cfg:  Config{RepoDir: ".", Logger: logger},
		tmux: tmux.NewClient(tmuxRunner, logger),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		sessionStore:           daemonstate.NewStore(),
		runtimeStoresByRoot:    map[string]*daemonstate.RuntimeStateStore{},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore},
		revision:               map[string]uint64{projectID: 9},
	}

	body, err := json.Marshal(map[string][]string{
		"task_ids": []string{secondID, "az-missing", firstID},
	})
	if err != nil {
		t.Fatalf("marshal get-many request: %v", err)
	}
	resp, err := d.handleTaskGetMany(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-get-many",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.get_many",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskGetMany error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.get_many response = %+v", resp.Error)
	}

	payload, err := protocol.DecodeTaskListSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatalf("decode task.get_many body: %v", err)
	}
	if got, want := payload.SnapshotRevision, uint64(9); got != want {
		t.Fatalf("snapshot revision = %d, want %d", got, want)
	}
	taskByID := map[string]domain.Task{}
	for _, task := range payload.Tasks {
		taskByID[task.ID.String()] = task
	}
	if _, ok := taskByID["az-missing"]; ok {
		t.Fatalf("missing issue appeared in payload: %+v", payload.Tasks)
	}
	second := taskByID[secondID]
	if got, want := len(second.Dependencies), 1; got != want {
		t.Fatalf("second dependencies = %+v, want one", second.Dependencies)
	}
	if second.Dependencies[0].ID.String() != firstID {
		t.Fatalf("second dependency id = %q, want %q", second.Dependencies[0].ID, firstID)
	}
	if _, ok := taskByID[firstID]; !ok {
		t.Fatalf("dependency context missing first issue: %+v", payload.Tasks)
	}
	for _, row := range []struct {
		name      string
		sessionID string
	}{
		{name: "requested", sessionID: secondSessionID},
		{name: "context", sessionID: firstSessionID},
	} {
		session, found, err := runtimeStore.GetSessionState(ctx, projectID, row.sessionID)
		if err != nil {
			t.Fatalf("load %s session state: %v", row.name, err)
		}
		if !found {
			t.Fatalf("%s session state missing", row.name)
		}
		if session.ObservedState != daemonstate.SessionStateRunning {
			t.Fatalf("%s observed state = %s, want running", row.name, session.ObservedState)
		}
		if !session.UpdatedAt.After(staleUpdatedAt) {
			t.Fatalf("%s updated_at = %s, want after %s", row.name, session.UpdatedAt, staleUpdatedAt)
		}
	}
}

func TestRefreshWorktreeRuntimeStateForIssuesDoesNotPublishUnchangedGitStatus(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()

	const (
		projectID = "proj-refresh-issue"
		issueID   = "az-1"
		worktree  = "/tmp/proj-refresh-issue-az-1"
		branch    = "riordan/az-1/refresh"
	)
	status := cleanGitStatus()
	rawStatus, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), logger)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	if err := runtimeStateStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   issueID,
		Path:      worktree,
		Branch:    branch,
		UpdatedAt: time.Date(2026, time.April, 2, 11, 1, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}
	if err := runtimeStateStore.UpsertWorktreeStateGitStatus(ctx, projectID, issueID, rawStatus, time.Date(2026, time.April, 2, 11, 2, 0, 0, time.UTC)); err != nil {
		t.Fatalf("seed git status projection: %v", err)
	}

	runner := &recordingGitRunner{
		runFn: func(args ...string) (string, error) {
			switch {
			case len(args) == 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain":
				return "worktree " + worktree + "\nbranch refs/heads/" + branch + "\n\n", nil
			case len(args) >= 4 && args[0] == "-C" && args[1] == worktree && args[2] == "status" && args[3] == "--porcelain":
				return "", nil
			case len(args) >= 4 && args[0] == "-C" && args[1] == worktree && args[2] == "symbolic-ref":
				return "origin/main\n", nil
			case len(args) >= 4 && args[0] == "-C" && args[1] == worktree && args[2] == "merge-base":
				return "merge-base-sha\n", nil
			case len(args) >= 7 && args[0] == "-C" && args[1] == worktree && args[2] == "diff" && args[3] == "--shortstat":
				return "", nil
			case len(args) >= 4 && args[0] == "-C" && args[1] == worktree && args[2] == "rev-list":
				return "0\n", nil
			default:
				t.Fatalf("unexpected git args: %v", args)
				return "", nil
			}
		},
	}

	d := &Daemon{
		cfg: Config{RepoDir: ".", BaseBranch: "main", Logger: logger},
		hub: publish.NewHub(16, 8, logger),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(runner, ".", logger),
		},
		git: git.NewClient(runner, logger),
	}

	ch, cancel := d.hub.Subscribe(projectID, 0)
	defer cancel()

	if _, err := d.refreshWorktreeRuntimeStateForIssues(ctx, projectID, []string{issueID}); err != nil {
		t.Fatalf("refreshWorktreeRuntimeStateForIssues: %v", err)
	}
	cancel()
	for evt := range ch {
		if evt.Event == protocol.EventGitStatusUpdated {
			t.Fatalf("unexpected unchanged git status event: %+v", evt)
		}
	}
}

func TestTaskListSnapshotFreshnessRefreshesSessionCacheBeforeEvaluation(t *testing.T) {
	originalNow := timeNow
	t.Cleanup(func() {
		timeNow = originalNow
	})
	timeNow = func() time.Time {
		return time.Date(2026, time.April, 2, 11, 5, 0, 0, time.UTC)
	}

	ctx := context.Background()
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStore.Close() })

	const (
		projectID = "proj-fresh"
		issueID   = "az-1"
	)
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	durableUpdatedAt := time.Date(2026, time.April, 2, 11, 4, 45, 0, time.UTC)
	if err := runtimeStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStateAttached,
		UpdatedAt: durableUpdatedAt,
	}); err != nil {
		t.Fatalf("seed durable session projection: %v", err)
	}

	sessionStore := daemonstate.NewStore()
	sessionStore.ReplaceProjectSessions(projectID, []daemonstate.Session{{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStateAttached,
		UpdatedAt: time.Date(2026, time.April, 2, 10, 0, 0, 0, time.UTC),
	}})

	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: slog.Default()},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStore,
		},
		sessionStore: sessionStore,
	}

	lastCheckedAt, freshness := d.taskListSnapshotFreshness(ctx, projectID)
	if !lastCheckedAt.Equal(durableUpdatedAt) {
		t.Fatalf("lastCheckedAt = %v, want %v from durable projection", lastCheckedAt, durableUpdatedAt)
	}
	if freshness != protocol.TaskListFreshnessFresh {
		t.Fatalf("freshness = %q, want %q", freshness, protocol.TaskListFreshnessFresh)
	}
}

func TestHandleTaskListDoesNotPersistSessionProjectionSnapshot(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() {
		_ = issuesClient.CloseDB()
	})
	if _, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "task list no projection writes",
		Type:  domain.TypeTask,
	}); err != nil {
		t.Fatalf("create issue: %v", err)
	}

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	projectID := "proj-tasklist-no-write"
	sessionID := naming.CanonicalSessionID(projectID, "az-1")
	tmuxRunner := &testTmuxRunner{
		sessions: map[string]bool{
			sessionID: true,
		},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}

	d := &Daemon{
		cfg:          Config{RepoDir: repoDir, Logger: slog.Default()},
		issues:       issuesClient,
		sessionStore: daemonstate.NewStore(),
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
	}

	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-list-no-write",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.list",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
	}
	resp, err := d.handleTaskList(ctx, req)
	if err != nil {
		t.Fatalf("handleTaskList returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.list response not OK: %+v", resp)
	}

	rows, err := runtimeStateStore.ListSessionStates(ctx, projectID)
	if err != nil {
		t.Fatalf("ListSessions returned error: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("projection rows = %d, want 0 (task.list read path must not persist)", len(rows))
	}
}

func TestHandleTaskGetManyIncludesAncestorContextWhenRequested(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	issuesClient := issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	projectID := "proj-get-many-ancestors"
	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Root",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create root issue: %v", err)
	}
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Parent",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create parent issue: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Child",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child issue: %v", err)
	}
	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		sessionStore: daemonstate.NewStore(),
		revision:     map[string]uint64{projectID: 11},
	}
	body, err := json.Marshal(map[string]any{
		"task_ids":          []string{childID},
		"include_ancestors": true,
	})
	if err != nil {
		t.Fatalf("marshal get-many request: %v", err)
	}

	resp, err := d.handleTaskGetMany(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-get-many-ancestors",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.get_many",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskGetMany error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.get_many response = %+v", resp.Error)
	}
	payload, err := protocol.DecodeTaskListSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatalf("decode task.get_many body: %v", err)
	}
	taskByID := map[string]domain.Task{}
	for _, task := range payload.Tasks {
		taskByID[task.ID.String()] = task
	}
	for _, issueID := range []string{childID, parentID, rootID} {
		if _, ok := taskByID[issueID]; !ok {
			t.Fatalf("task %s missing from ancestor-context payload: %+v", issueID, payload.Tasks)
		}
	}
}

func TestHandleTaskGetManyCanExcludeDependents(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	issuesClient := issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	projectID := "proj-get-many-no-dependents"
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Parent",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create parent issue: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Child",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child issue: %v", err)
	}
	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		sessionStore: daemonstate.NewStore(),
		revision:     map[string]uint64{projectID: 12},
	}
	body, err := json.Marshal(map[string]any{
		"task_ids":           []string{parentID},
		"exclude_dependents": true,
	})
	if err != nil {
		t.Fatalf("marshal get-many request: %v", err)
	}

	resp, err := d.handleTaskGetMany(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-get-many-no-dependents",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.get_many",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskGetMany error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.get_many response = %+v", resp.Error)
	}
	payload, err := protocol.DecodeTaskListSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatalf("decode task.get_many body: %v", err)
	}
	taskByID := map[string]domain.Task{}
	for _, task := range payload.Tasks {
		taskByID[task.ID.String()] = task
	}
	if _, ok := taskByID[parentID]; !ok {
		t.Fatalf("parent task missing from payload: %+v", payload.Tasks)
	}
	if _, ok := taskByID[childID]; ok {
		t.Fatalf("dependent child task should be omitted from payload: %+v", payload.Tasks)
	}
}

func TestHandleTaskGetManyMetadataOnlyPreservesContextShape(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	issuesClient := issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	projectID := "proj-get-many-metadata-only"
	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:       "Root",
		Description: "root details should stay out of metadata-only payload",
		Type:        domain.TypeTask,
		Priority:    domain.P2,
		Status:      domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create root issue: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:       "Child",
		Description: "child details should stay out of metadata-only payload",
		Type:        domain.TypeTask,
		Priority:    domain.P2,
		Status:      domain.StatusOpen,
		ParentID:    &rootID,
	})
	if err != nil {
		t.Fatalf("create child issue: %v", err)
	}
	relatedID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Related dependency",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create related issue: %v", err)
	}
	if err := issuesClient.AddDependency(ctx, childID, relatedID, string(domain.DependencyRelatedTo)); err != nil {
		t.Fatalf("add related dependency: %v", err)
	}
	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		sessionStore: daemonstate.NewStore(),
		revision:     map[string]uint64{projectID: 12},
	}
	body, err := json.Marshal(map[string]any{
		"task_ids":           []string{childID},
		"include_ancestors":  true,
		"exclude_dependents": true,
		"metadata_only":      true,
	})
	if err != nil {
		t.Fatalf("marshal get-many request: %v", err)
	}

	resp, err := d.handleTaskGetMany(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-get-many-metadata-only",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         "task.get_many",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleTaskGetMany error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.get_many response = %+v", resp.Error)
	}
	payload, err := protocol.DecodeTaskListSnapshotPayload(resp.Body)
	if err != nil {
		t.Fatalf("decode task.get_many body: %v", err)
	}
	taskByID := map[string]domain.Task{}
	for _, task := range payload.Tasks {
		taskByID[task.ID.String()] = task
	}
	for _, issueID := range []string{childID, rootID} {
		if _, ok := taskByID[issueID]; !ok {
			t.Fatalf("task %s missing from metadata-only payload: %+v", issueID, payload.Tasks)
		}
	}
	if _, ok := taskByID[relatedID]; ok {
		t.Fatalf("metadata-only payload included unrelated dependency %s: %+v", relatedID, payload.Tasks)
	}
	if taskByID[childID].Description != "" {
		t.Fatalf("metadata-only child description = %q, want empty", taskByID[childID].Description)
	}
	if taskByID[childID].ParentID == nil || taskByID[childID].ParentID.String() != rootID {
		t.Fatalf("metadata-only child parent = %+v, want %s", taskByID[childID].ParentID, rootID)
	}
}

func TestHandleTaskSnapshotExportUsesProjectionSessions(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() {
		_ = issuesClient.CloseDB()
	})
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "snapshot export projection sessions",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	projectID := "proj-snapshot-export"
	sessionID := naming.CanonicalSessionID(projectID, taskID)
	if err := runtimeStateStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   taskID,
		State:     daemonstate.SessionStateAttached,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed projection session: %v", err)
	}

	// Keep tmux empty; export should still include the projected session.
	tmuxRunner := &testTmuxRunner{
		sessions:    map[string]bool{},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}

	d := &Daemon{
		cfg:          Config{RepoDir: repoDir, Logger: slog.Default()},
		issues:       issuesClient,
		sessionStore: daemonstate.NewStore(),
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
	}

	resp, err := d.handleTaskSnapshotExport(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-snapshot-export",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.snapshot.export",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
	})
	if err != nil {
		t.Fatalf("handleTaskSnapshotExport error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.snapshot.export response not OK: %+v", resp.Error)
	}

	var out taskSnapshotExportBody
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		t.Fatalf("unmarshal snapshot export body: %v", err)
	}
	if out.ProjectID != projectID {
		t.Fatalf("project id = %q, want %q", out.ProjectID, projectID)
	}
	if out.TaskCount != 1 {
		t.Fatalf("task count = %d, want 1", out.TaskCount)
	}
	if out.SessionCount != 1 {
		t.Fatalf("session count = %d, want 1", out.SessionCount)
	}
	if len(out.Sessions) != 1 || out.Sessions[0].Name != sessionID {
		t.Fatalf("sessions = %+v, want [%s]", out.Sessions, sessionID)
	}
	if len(out.Tasks) != 1 || !out.Tasks[0].SessionAttached {
		t.Fatalf("tasks = %+v, expected exported task with SessionAttached=true", out.Tasks)
	}
}

func TestHandleTaskGetEnqueuesOnlyRequestedIssueWorktreeRefreshAsync(t *testing.T) {
	ctx := context.Background()
	projectID := protocol.DefaultProjectID
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	targetID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "target issue",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create target issue: %v", err)
	}
	otherID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "other issue",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create other issue: %v", err)
	}

	targetWorktree := filepath.Join(repoDir, "target-worktree")
	otherWorktree := filepath.Join(repoDir, "other-worktree")
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	for _, row := range []daemonstate.WorktreeState{
		{ProjectID: projectID, IssueID: targetID, Path: targetWorktree, Branch: "az/" + targetID, UpdatedAt: time.Now().UTC()},
		{ProjectID: projectID, IssueID: otherID, Path: otherWorktree, Branch: "az/" + otherID, UpdatedAt: time.Now().UTC()},
	} {
		if err := store.UpsertWorktreeState(ctx, row); err != nil {
			t.Fatalf("seed worktree state: %v", err)
		}
	}

	statusPaths := make(chan string, 4)
	statusRelease := make(chan struct{})
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		if len(args) >= 4 && args[0] == "-C" && args[2] == "status" && args[3] == "--porcelain" {
			statusPaths <- args[1]
			<-statusRelease
			return " M changed.go\n", nil
		}
		return "", nil
	}}
	queue := newReconcileQueue[*git.GitStatus](reconcileQueueConfig{
		Name:    "test_git_status_refresh",
		Workers: 1,
		Logger:  slog.Default(),
	})
	t.Cleanup(func() { _ = queue.Close() })
	gitAdapter := &gitServiceAdapter{
		client:             git.NewClient(runner, slog.Default()),
		runtimeStateStore:  store,
		statusRefreshQueue: queue,
		logger:             slog.Default(),
		baseBranch:         "main",
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore {
			return store
		},
	}
	d := &Daemon{
		cfg:              Config{BaseBranch: "main", Logger: slog.Default()},
		issues:           issuesClient,
		gitStatusAdapter: gitAdapter,
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
	}

	reqBody, err := json.Marshal(map[string]string{"task_id": targetID})
	if err != nil {
		t.Fatalf("marshal task get request: %v", err)
	}
	type taskGetResult struct {
		resp protocol.ResponseEnvelope
		err  error
	}
	resultCh := make(chan taskGetResult, 1)
	go func() {
		resp, err := d.handleTaskGet(ctx, protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-task-get-refresh",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         "task.get",
			Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
			Body:            reqBody,
		})
		resultCh <- taskGetResult{resp: resp, err: err}
	}()

	select {
	case got := <-statusPaths:
		if got != targetWorktree {
			t.Fatalf("refreshed worktree = %q, want %q", got, targetWorktree)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for target issue worktree refresh")
	}

	var result taskGetResult
	select {
	case result = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("task.get did not return while git status refresh was still running")
	}
	if result.err != nil {
		t.Fatalf("handleTaskGet returned error: %v", result.err)
	}
	if !result.resp.OK {
		t.Fatalf("task.get response not OK: %+v", result.resp.Error)
	}

	payload, err := protocol.DecodeTaskListSnapshotPayload(result.resp.Body)
	if err != nil {
		t.Fatalf("decode task.get body: %v", err)
	}
	if len(payload.Tasks) != 1 {
		t.Fatalf("response task count = %d, want 1", len(payload.Tasks))
	}
	close(statusRelease)

	select {
	case got := <-statusPaths:
		t.Fatalf("unexpected extra worktree refresh for %q", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestHandleTaskGetRefreshesOnlyRequestedIssueSession(t *testing.T) {
	ctx := context.Background()
	projectID := "azedarach"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	_, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "schema seed",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create schema seed issue: %v", err)
	}

	targetID := "bra"
	contextID := "brb"
	dbPath := filepath.Join(repoDir, ".azedarach", "azedarach.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, row := range []struct {
		id    string
		title string
	}{
		{id: targetID, title: "target issue"},
		{id: contextID, title: "context issue"},
	} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO issues (
				id, title, description, status, priority, issue_type,
				created_at, updated_at, closed_at, assignee, labels_json,
				implementations_json, design, notes, acceptance, estimate, deleted_at
			)
			VALUES (?, ?, NULL, ?, ?, ?, ?, ?, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL)
		`, row.id, row.title, string(domain.StatusOpen), int(domain.P3), string(domain.TypeTask), now, now); err != nil {
			t.Fatalf("insert issue %s: %v", row.id, err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO issue_dependencies (issue_id, depends_on_id, dependency_type, tombstoned_at)
		VALUES (?, ?, ?, NULL)
	`, contextID, targetID, string(domain.DependencyBlocks)); err != nil {
		t.Fatalf("insert context dependency: %v", err)
	}

	store := daemonstate.NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = store.Close() })

	targetSessionID := naming.CanonicalSessionID(projectID, targetID)
	contextSessionID := naming.CanonicalSessionID(projectID, contextID)
	targetUpdatedAt := time.Date(2026, time.April, 2, 10, 0, 0, 0, time.UTC)
	for _, row := range []struct {
		name      string
		sessionID string
		issueID   string
	}{
		{name: "target", sessionID: targetSessionID, issueID: targetSessionID},
		{name: "context", sessionID: contextSessionID, issueID: contextID},
	} {
		if err := store.UpsertSessionState(ctx, projectID, daemonstate.Session{
			ID:            row.sessionID,
			IssueID:       row.issueID,
			State:         daemonstate.SessionStateRunning,
			ObservedState: daemonstate.SessionStateStopped,
			UpdatedAt:     targetUpdatedAt,
		}); err != nil {
			t.Fatalf("seed %s session state: %v", row.name, err)
		}
	}

	otherUpdatedAt := time.Date(2026, time.April, 2, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 25; i++ {
		issueID := fmt.Sprintf("other-%02d", i)
		if err := store.UpsertSessionState(ctx, projectID, daemonstate.Session{
			ID:            naming.CanonicalSessionID(projectID, issueID),
			IssueID:       issueID,
			State:         daemonstate.SessionStateStopped,
			ObservedState: daemonstate.SessionStateStopped,
			UpdatedAt:     otherUpdatedAt,
		}); err != nil {
			t.Fatalf("seed other session state %d: %v", i, err)
		}
	}

	tmuxRunner := &testTmuxRunner{
		sessions: map[string]bool{
			targetSessionID:  true,
			contextSessionID: true,
		},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}
	d := &Daemon{
		cfg:    Config{Logger: slog.Default()},
		issues: issuesClient,
		tmux:   tmux.NewClient(tmuxRunner, slog.Default()),
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
	}

	reqBody, err := json.Marshal(map[string]string{"task_id": targetID})
	if err != nil {
		t.Fatalf("marshal task get request: %v", err)
	}
	resp, err := d.handleTaskGet(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-get-session-refresh",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.get",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            reqBody,
	})
	if err != nil {
		t.Fatalf("handleTaskGet returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.get response not OK: %+v", resp.Error)
	}

	for _, row := range []struct {
		name      string
		sessionID string
	}{
		{name: "target", sessionID: targetSessionID},
		{name: "context", sessionID: contextSessionID},
	} {
		session, found, err := store.GetSessionState(ctx, projectID, row.sessionID)
		if err != nil {
			t.Fatalf("load %s session state: %v", row.name, err)
		}
		if !found {
			t.Fatalf("%s session state missing", row.name)
		}
		if session.ObservedState != daemonstate.SessionStateRunning {
			t.Fatalf("%s observed state = %s, want running", row.name, session.ObservedState)
		}
		if !session.UpdatedAt.After(targetUpdatedAt) {
			t.Fatalf("%s updated_at = %s, want after %s", row.name, session.UpdatedAt, targetUpdatedAt)
		}
	}

	sessions, err := store.ListSessionStates(ctx, projectID)
	if err != nil {
		t.Fatalf("list session states: %v", err)
	}
	for _, session := range sessions {
		if session.ID == targetSessionID || session.ID == contextSessionID {
			continue
		}
		if !session.UpdatedAt.Equal(otherUpdatedAt) {
			t.Fatalf("unrelated session %s updated_at = %s, want unchanged %s", session.ID, session.UpdatedAt, otherUpdatedAt)
		}
	}
}

func TestRefreshWorktreeRuntimeStatePersistsGitMetricsFromWorktreeList(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-refresh-worktrees"
	worktreePath := "/tmp/repo-az-1"
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain":
			return "worktree /tmp/repo-root\nbranch refs/heads/main\n\nworktree /tmp/repo-az-1\nbranch refs/heads/az/az-1\n", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == worktreePath && args[2] == "status" && args[3] == "--porcelain":
			return " M changed.go\n?? new.go\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == worktreePath && args[2] == "merge-base" && args[3] == "main" && args[4] == "HEAD":
			return "abc123\n", nil
		case len(args) >= 8 && args[0] == "-C" && args[1] == worktreePath && args[2] == "diff" && args[3] == "--shortstat" && args[4] == "abc123" && args[5] == "HEAD":
			return " 2 files changed, 7 insertions(+), 3 deletions(-)\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == worktreePath && args[2] == "rev-list" && args[3] == "--count" && args[4] == "HEAD..main":
			return "5\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == worktreePath && args[2] == "rev-list" && args[3] == "--count" && args[4] == "main..HEAD":
			return "2\n", nil
		default:
			return "", nil
		}
	}}

	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })

	d := &Daemon{
		cfg: Config{RepoDir: "/tmp/repo-root", BaseBranch: "main", Logger: slog.Default()},
		git: git.NewClient(runner, slog.Default()),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			"/tmp/repo-root": store,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			"/tmp/repo-root": git.NewWorktreeManager(runner, "/tmp/repo-root", slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(runner, "/tmp/repo-root", slog.Default()),
		},
	}

	count, err := d.refreshWorktreeRuntimeState(ctx, projectID)
	if err != nil {
		t.Fatalf("refreshWorktreeRuntimeState error: %v", err)
	}
	if count != 1 {
		t.Fatalf("refreshWorktreeRuntimeState count = %d, want 1", count)
	}

	worktrees, err := store.ListWorktreeStates(ctx, projectID)
	if err != nil {
		t.Fatalf("ListWorktrees error: %v", err)
	}
	if len(worktrees) != 1 {
		t.Fatalf("ListWorktrees len = %d, want 1", len(worktrees))
	}

	var status git.GitStatus
	if err := json.Unmarshal(worktrees[0].GitStatusRaw, &status); err != nil {
		t.Fatalf("unmarshal persisted git status: %v", err)
	}
	if !status.HasChanges {
		t.Fatal("persisted git status should be dirty")
	}
	if status.GitAdditions != 7 || status.GitDeletions != 3 {
		t.Fatalf("persisted git diff totals = %d/%d, want 7/3", status.GitAdditions, status.GitDeletions)
	}
	if status.GitAheadCount != 2 || status.GitBehindCount != 5 {
		t.Fatalf("persisted git ahead/behind = %d/%d, want 2/5", status.GitAheadCount, status.GitBehindCount)
	}
}

func TestRefreshWorktreeRuntimeStateSkipsClosedIssueWorktrees(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-refresh-closed-worktrees"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	closedID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "closed with stale live worktree",
		Type:   domain.TypeTask,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := issuesClient.Update(ctx, closedID, domain.StatusDone); err != nil {
		t.Fatalf("close issue: %v", err)
	}

	worktreePath := filepath.Join(repoDir, "wt-"+closedID)
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain" {
			return strings.Join([]string{
				"worktree " + repoDir,
				"branch refs/heads/main",
				"",
				"worktree " + worktreePath,
				"branch refs/heads/az/" + closedID,
				"",
			}, "\n"), nil
		}
		t.Fatalf("unexpected git args: %v", args)
		return "", nil
	}}

	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
	}

	count, err := d.refreshWorktreeRuntimeState(ctx, projectID)
	if err != nil {
		t.Fatalf("refreshWorktreeRuntimeState error: %v", err)
	}
	if count != 0 {
		t.Fatalf("refreshWorktreeRuntimeState count = %d, want 0", count)
	}
	if _, found, err := store.GetWorktreeStateByIssueID(ctx, projectID, closedID); err != nil {
		t.Fatalf("get worktree state: %v", err)
	} else if found {
		t.Fatalf("closed issue worktree projection persisted for %s", closedID)
	}
}

func TestRefreshWorktreeRuntimeStateSuppressesMissingWorktreeFromGitList(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-refresh-missing-worktree"
	repoDir := t.TempDir()
	missingID := "az-missing"
	missingWorktree := filepath.Join(repoDir, "missing-"+missingID)

	statusCalls := 0
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain":
			return strings.Join([]string{
				"worktree " + repoDir,
				"branch refs/heads/main",
				"",
				"worktree " + missingWorktree,
				"branch refs/heads/az/" + missingID,
				"",
			}, "\n"), nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "status" && args[3] == "--porcelain":
			statusCalls++
			return "", nil
		default:
			return "", nil
		}
	}}

	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	if err := store.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   missingID,
		Path:      missingWorktree,
		Branch:    "az/" + missingID,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed stale worktree projection: %v", err)
	}
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: slog.Default()},
		git: git.NewClient(runner, slog.Default()),
		hub: publish.NewHub(16, 8, slog.Default()),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: store,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
	}
	ch, cancel := d.hub.Subscribe(projectID, 0)
	t.Cleanup(cancel)

	count, err := d.refreshWorktreeRuntimeState(ctx, projectID)
	if err != nil {
		t.Fatalf("refreshWorktreeRuntimeState error: %v", err)
	}
	if count != 0 {
		t.Fatalf("refreshWorktreeRuntimeState count = %d, want 0", count)
	}
	if statusCalls != 0 {
		t.Fatalf("status calls = %d, want 0 for missing stale worktree", statusCalls)
	}
	if _, found, err := store.GetWorktreeStateByIssueID(ctx, projectID, missingID); err != nil {
		t.Fatalf("get worktree state: %v", err)
	} else if found {
		t.Fatalf("missing worktree projection still present for %s", missingID)
	}
	select {
	case evt := <-ch:
		if evt.Event != protocol.EventWorktreeProjectionUpdated {
			t.Fatalf("event = %s, want %s", evt.Event, protocol.EventWorktreeProjectionUpdated)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for missing worktree projection delete event")
	}
}

func TestRefreshWorktreeRuntimeStateForIssuesDeletesClosedIssueWorktreeProjection(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-refresh-closed-target"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	closedID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "closed targeted worktree",
		Type:   domain.TypeTask,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := issuesClient.Update(ctx, closedID, domain.StatusDone); err != nil {
		t.Fatalf("close issue: %v", err)
	}

	worktreePath := filepath.Join(repoDir, "wt-"+closedID)
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain" {
			return strings.Join([]string{
				"worktree " + repoDir,
				"branch refs/heads/main",
				"",
				"worktree " + worktreePath,
				"branch refs/heads/az/" + closedID,
				"",
			}, "\n"), nil
		}
		t.Fatalf("unexpected git args: %v", args)
		return "", nil
	}}

	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
	}

	count, err := d.refreshWorktreeRuntimeStateForIssues(ctx, projectID, []string{closedID})
	if err != nil {
		t.Fatalf("refreshWorktreeRuntimeStateForIssues error: %v", err)
	}
	if count != 0 {
		t.Fatalf("refreshWorktreeRuntimeStateForIssues count = %d, want 0", count)
	}
	if _, found, err := store.GetWorktreeStateByIssueID(ctx, projectID, closedID); err != nil {
		t.Fatalf("get worktree state: %v", err)
	} else if found {
		t.Fatalf("closed issue worktree projection persisted for %s", closedID)
	}
}

func TestRuntimeWorktreeIssueEligibleRejectsClosedAncestor(t *testing.T) {
	parentID := naming.IssueID("az-parent")
	childID := naming.IssueID("az-child")
	taskByIssue := map[string]domain.Task{
		parentID.String(): {
			ID:     parentID,
			Status: domain.StatusDone,
			Type:   domain.TypeTask,
		},
		childID.String(): {
			ID:       childID,
			Status:   domain.StatusInProgress,
			Type:     domain.TypeTask,
			ParentID: &parentID,
		},
	}

	if runtimeWorktreeIssueEligible(childID.String(), taskByIssue) {
		t.Fatal("child under closed ancestor should not be runtime-worktree eligible")
	}
}

func TestRefreshWorktreeRuntimeStateUsesClosestNonDoneAncestorBranch(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-refresh-ancestor-base"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "root",
		Type:   domain.TypeTask,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "parent",
		Type:     domain.TypeTask,
		ParentID: &rootID,
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "child",
		Type:     domain.TypeTask,
		Status:   domain.StatusInProgress,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}

	childWorktreePath := filepath.Join(repoDir, "wt-child")
	var mergeBaseRef string
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain":
			return strings.Join([]string{
				"worktree " + repoDir,
				"branch refs/heads/main",
				"",
				"worktree " + filepath.Join(repoDir, "wt-root"),
				"branch refs/heads/az/" + rootID,
				"",
				"worktree " + filepath.Join(repoDir, "wt-parent"),
				"branch refs/heads/az/" + parentID,
				"",
				"worktree " + childWorktreePath,
				"branch refs/heads/az/" + childID,
				"",
			}, "\n"), nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == childWorktreePath && args[2] == "merge-base":
			mergeBaseRef = args[3]
			return "abc123\n", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == childWorktreePath && args[2] == "status" && args[3] == "--porcelain":
			return " M changed.go\n", nil
		case len(args) >= 8 && args[0] == "-C" && args[1] == childWorktreePath && args[2] == "diff" && args[3] == "--shortstat":
			return " 1 file changed, 2 insertions(+), 1 deletion(-)\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == childWorktreePath && args[2] == "rev-list" && args[3] == "--count":
			return "0\n", nil
		default:
			return "", nil
		}
	}}

	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, "projection.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })

	d := &Daemon{
		cfg:    Config{RepoDir: repoDir, BaseBranch: "main", Logger: slog.Default()},
		git:    git.NewClient(runner, slog.Default()),
		issues: issuesClient,
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: store,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
	}

	_, err = d.refreshWorktreeRuntimeState(ctx, projectID)
	if err != nil {
		t.Fatalf("refreshWorktreeRuntimeState error: %v", err)
	}
	if got, want := strings.TrimSpace(mergeBaseRef), "az/"+parentID; got != want {
		t.Fatalf("merge-base base ref = %q, want %q", got, want)
	}
}

func TestRuntimeDiffBaseBranchForIssueUsesNearestAncestorWorktreeEvenWhenClosed(t *testing.T) {
	childID := "az-child"
	parentID := "az-parent"
	rootID := "az-root"
	parentIssueID := naming.IssueID(parentID)
	rootIssueID := naming.IssueID(rootID)

	taskByIssue := map[string]domain.Task{
		childID: {
			ID:       naming.IssueID(childID),
			Status:   domain.StatusInProgress,
			ParentID: &parentIssueID,
		},
		parentID: {
			ID:       parentIssueID,
			Status:   domain.StatusDone,
			ParentID: &rootIssueID,
		},
		rootID: {
			ID:     rootIssueID,
			Status: domain.StatusInProgress,
		},
	}
	worktreeByIssue := map[string]git.Worktree{
		parentID: {
			IssueID: parentID,
			Branch:  "riordan/" + parentID + "/parent-branch",
		},
		rootID: {
			IssueID: rootID,
			Branch:  "riordan/" + rootID + "/root-branch",
		},
	}

	d := &Daemon{}
	branch := d.runtimeDiffBaseBranchForIssue(childID, "preview", taskByIssue, worktreeByIssue)
	if branch != "riordan/"+parentID+"/parent-branch" {
		t.Fatalf("runtimeDiffBaseBranchForIssue(...) = %q, want nearest ancestor worktree branch", branch)
	}
}

func TestRuntimeDiffBaseBranchForIssueSkipsAncestorWithoutWorktree(t *testing.T) {
	childID := "az-child"
	parentID := "az-parent"
	rootID := "az-root"
	parentIssueID := naming.IssueID(parentID)
	rootIssueID := naming.IssueID(rootID)

	taskByIssue := map[string]domain.Task{
		childID: {
			ID:       naming.IssueID(childID),
			Status:   domain.StatusInProgress,
			ParentID: &parentIssueID,
		},
		parentID: {
			ID:       parentIssueID,
			Status:   domain.StatusInProgress,
			ParentID: &rootIssueID,
		},
		rootID: {
			ID:     rootIssueID,
			Status: domain.StatusInProgress,
		},
	}
	worktreeByIssue := map[string]git.Worktree{
		rootID: {
			IssueID: rootID,
			Branch:  "riordan/" + rootID + "/root-branch",
		},
	}

	d := &Daemon{}
	branch := d.runtimeDiffBaseBranchForIssue(childID, "preview", taskByIssue, worktreeByIssue)
	if branch != "riordan/"+rootID+"/root-branch" {
		t.Fatalf("runtimeDiffBaseBranchForIssue(...) = %q, want closest ancestor worktree branch", branch)
	}
}

func TestRuntimeDiffBaseBranchForWorktreeUsesClosestAncestorWorktree(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-worktree-base"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "parent",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "child",
		Type:     domain.TypeTask,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}

	parentWorktree := filepath.Join(repoDir, "wt-parent")
	childWorktree := filepath.Join(repoDir, "wt-child")
	parentBranch := "riordan/" + parentID + "/parent-branch"
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	for _, row := range []daemonstate.WorktreeState{
		{ProjectID: projectID, IssueID: parentID, Path: parentWorktree, Branch: parentBranch, UpdatedAt: time.Now().UTC()},
		{ProjectID: projectID, IssueID: childID, Path: childWorktree, Branch: "riordan/" + childID + "/child-branch", UpdatedAt: time.Now().UTC()},
	} {
		if err := store.UpsertWorktreeState(ctx, row); err != nil {
			t.Fatalf("seed worktree state: %v", err)
		}
	}

	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain" {
			return strings.Join([]string{
				"worktree " + repoDir,
				"branch refs/heads/preview",
				"",
				"worktree " + parentWorktree,
				"branch refs/heads/" + parentBranch,
				"",
				"worktree " + childWorktree,
				"branch refs/heads/riordan/" + childID + "/child-branch",
				"",
			}, "\n"), nil
		}
		t.Fatalf("unexpected git args: %v", args)
		return "", nil
	}}
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "preview", Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: store,
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
	}

	branch := d.runtimeDiffBaseBranchForWorktree(ctx, projectID, childWorktree)
	if branch != parentBranch {
		t.Fatalf("runtimeDiffBaseBranchForWorktree(...) = %q, want closest ancestor worktree branch %q", branch, parentBranch)
	}
}

func TestRefreshWorktreeRuntimeStateFallsBackToAncestorWorktreeBranchWhenAncestorTaskMissing(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-refresh-ancestor-worktree-fallback"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })

	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "child",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}

	ancestorID := "az-ancestor"
	childWorktreePath := filepath.Join(repoDir, "wt-child")
	var mergeBaseRef string
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain":
			return strings.Join([]string{
				"worktree " + repoDir,
				"branch refs/heads/main",
				"",
				"worktree " + filepath.Join(repoDir, "wt-ancestor"),
				"branch refs/heads/riordan/" + ancestorID + "/ancestor-branch",
				"",
				"worktree " + childWorktreePath,
				"branch refs/heads/riordan/" + childID + "/child-branch",
				"",
			}, "\n"), nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == childWorktreePath && args[2] == "merge-base":
			mergeBaseRef = args[3]
			return "abc123\n", nil
		case len(args) >= 4 && args[0] == "-C" && args[1] == childWorktreePath && args[2] == "status" && args[3] == "--porcelain":
			return " M changed.go\n", nil
		case len(args) >= 8 && args[0] == "-C" && args[1] == childWorktreePath && args[2] == "diff" && args[3] == "--shortstat":
			return " 1 file changed, 1 insertion(+), 1 deletion(-)\n", nil
		case len(args) >= 5 && args[0] == "-C" && args[1] == childWorktreePath && args[2] == "rev-list" && args[3] == "--count":
			return "0\n", nil
		default:
			return "", nil
		}
	}}

	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, "projection.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })

	d := &Daemon{
		cfg:    Config{RepoDir: repoDir, BaseBranch: "main", Logger: slog.Default()},
		git:    git.NewClient(runner, slog.Default()),
		issues: issuesClient,
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: store,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
	}

	// Simulate sparse task projection where child references an ancestor not in taskByIssue.
	taskByIssue := map[string]domain.Task{
		childID: {
			ID:       naming.IssueID(childID),
			ParentID: func() *naming.IssueID { v := naming.IssueID(ancestorID); return &v }(),
		},
	}
	branch := d.runtimeDiffBaseBranchForIssue(childID, "main", taskByIssue, map[string]git.Worktree{
		ancestorID: {
			IssueID: ancestorID,
			Branch:  "riordan/" + ancestorID + "/ancestor-branch",
		},
	})
	if branch != "riordan/"+ancestorID+"/ancestor-branch" {
		t.Fatalf("runtimeDiffBaseBranchForIssue(...) = %q, want ancestor worktree branch", branch)
	}

	_, err = d.refreshWorktreeRuntimeState(ctx, projectID)
	if err != nil {
		t.Fatalf("refreshWorktreeRuntimeState error: %v", err)
	}
	if got, want := strings.TrimSpace(mergeBaseRef), "main"; got != want {
		// Full reconcile has no persisted parent relation in this test DB shape,
		// so Az ancestry correctly falls back to configured base branch there.
		t.Fatalf("merge-base base ref = %q, want %q", got, want)
	}
}

func TestRefreshWorktreeRuntimeStateMutationDoesNotBypassGitProbeBudget(t *testing.T) {
	ctx := context.WithValue(context.Background(), runtimeReconcileRequestContextKey{}, runtimeReconcileRequestContext{
		Priority: reconcilePriorityManual,
		Reason:   "mutation:session.stop",
	})
	projectID := "proj-refresh-budget"
	repoDir := t.TempDir()
	worktreeOne := filepath.Join(repoDir, "repo-az-1")
	worktreeTwo := filepath.Join(repoDir, "repo-az-2")
	for _, path := range []string{worktreeOne, worktreeTwo} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir worktree %s: %v", path, err)
		}
	}
	statusCalls := 0
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain":
			return "worktree " + repoDir + "\nbranch refs/heads/main\n\n" +
				"worktree " + worktreeOne + "\nbranch refs/heads/az/az-1\n\n" +
				"worktree " + worktreeTwo + "\nbranch refs/heads/az/az-2\n", nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "status" && args[3] == "--porcelain":
			statusCalls++
			return "", nil
		default:
			return "", nil
		}
	}}

	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: slog.Default()},
		git: git.NewClient(runner, slog.Default()),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: store,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
		worktreeGitProbeThrottle: newReconcileThrottle(reconcileThrottleConfig{
			Name:                 "worktree_git_probe_test",
			Budget:               1,
			Cadence:              time.Hour,
			UnchangedBackoffBase: time.Hour,
			UnchangedBackoffMax:  time.Hour,
			FailureBackoffBase:   time.Hour,
			FailureBackoffMax:    time.Hour,
			Now:                  func() time.Time { return now },
		}),
	}

	count, err := d.refreshWorktreeRuntimeState(ctx, projectID)
	if err != nil {
		t.Fatalf("refreshWorktreeRuntimeState error: %v", err)
	}
	if count != 2 {
		t.Fatalf("refreshWorktreeRuntimeState count = %d, want 2", count)
	}
	if statusCalls != 1 {
		t.Fatalf("status calls = %d, want 1", statusCalls)
	}
}

func TestRefreshWorktreeRuntimeStateTransientGitFailureKeepsProjectionAndBacksOff(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-refresh-transient"
	repoDir := t.TempDir()
	worktreeOne := filepath.Join(repoDir, "repo-az-1")
	worktreeTwo := filepath.Join(repoDir, "repo-az-2")
	for _, path := range []string{worktreeOne, worktreeTwo} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir worktree %s: %v", path, err)
		}
	}

	statusCalls := 0
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain":
			return "worktree " + repoDir + "\nbranch refs/heads/main\n\n" +
				"worktree " + worktreeOne + "\nbranch refs/heads/az/az-1\n\n" +
				"worktree " + worktreeTwo + "\nbranch refs/heads/az/az-2\n", nil
		case len(args) >= 4 && args[0] == "-C" && args[2] == "status" && args[3] == "--porcelain":
			statusCalls++
			return "", errors.New("git status failed: exit status 128: index.lock busy")
		default:
			return "", nil
		}
	}}

	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: slog.Default()},
		git: git.NewClient(runner, slog.Default()),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: store,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(runner, repoDir, slog.Default()),
		},
		worktreeGitProbeThrottle: newReconcileThrottle(reconcileThrottleConfig{
			Name:                 "worktree_git_probe_test",
			Budget:               1,
			Cadence:              time.Hour,
			UnchangedBackoffBase: time.Hour,
			UnchangedBackoffMax:  time.Hour,
			FailureBackoffBase:   time.Hour,
			FailureBackoffMax:    time.Hour,
			Now:                  func() time.Time { return now },
		}),
	}

	count, err := d.refreshWorktreeRuntimeState(ctx, projectID)
	if err != nil {
		t.Fatalf("first refreshWorktreeRuntimeState error: %v", err)
	}
	if count != 2 {
		t.Fatalf("first refreshWorktreeRuntimeState count = %d, want 2", count)
	}
	count, err = d.refreshWorktreeRuntimeState(ctx, projectID)
	if err != nil {
		t.Fatalf("second refreshWorktreeRuntimeState error: %v", err)
	}
	if count != 2 {
		t.Fatalf("second refreshWorktreeRuntimeState count = %d, want 2", count)
	}
	if statusCalls != 1 {
		t.Fatalf("status calls = %d, want 1 after transient failure backoff/budget", statusCalls)
	}
	worktrees, err := store.ListWorktreeStates(ctx, projectID)
	if err != nil {
		t.Fatalf("list worktree projections: %v", err)
	}
	if len(worktrees) != 2 {
		t.Fatalf("worktree projection count = %d, want 2", len(worktrees))
	}
}

func TestWorktreeGitProbeThrottleKeyIncludesResolvedBaseBranch(t *testing.T) {
	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	throttle := newReconcileThrottle(reconcileThrottleConfig{
		Name:                 "worktree_git_probe_test",
		Cadence:              time.Hour,
		UnchangedBackoffBase: time.Hour,
		UnchangedBackoffMax:  time.Hour,
		FailureBackoffBase:   time.Hour,
		FailureBackoffMax:    time.Hour,
		Now:                  func() time.Time { return now },
	})

	oldBaseKey := worktreeGitProbeThrottleKey("proj", "/repo-dox", "preview")
	if !throttle.Admit(oldBaseKey, false).Allowed() {
		t.Fatal("initial preview-base probe was not admitted")
	}
	throttle.Record(oldBaseKey, "preview-large-diff", nil)
	if throttle.Admit(oldBaseKey, false).Allowed() {
		t.Fatal("same preview-base probe should be throttled after unchanged record")
	}

	parentBaseKey := worktreeGitProbeThrottleKey("proj", "/repo-dox", "riordan/cif/parent")
	if !throttle.Admit(parentBaseKey, false).Allowed() {
		t.Fatal("parent-base probe should not be suppressed by previous preview-base record")
	}
}
