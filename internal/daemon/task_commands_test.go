package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
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

	parentID := "parent-1"
	body := buildTaskSnapshotExportBody(
		"proj",
		17,
		[]domain.Task{
			{
				ID:           "b-task",
				Title:        "Bravo",
				Status:       domain.StatusBlocked,
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
	if body.Tasks[0].ParentID == nil || *body.Tasks[0].ParentID != parentID {
		t.Fatalf("Tasks[0].ParentID = %+v, want %q", body.Tasks[0].ParentID, parentID)
	}
	if got, want := body.Tasks[1].ID, "b-task"; got != want {
		t.Fatalf("Tasks[1].ID = %q, want %q", got, want)
	}
	if got, want := body.Tasks[1].Critical, true; got != want {
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

func TestEnrichTasksWithRuntimeProjectionCache(t *testing.T) {
	ctx := context.Background()
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })

	const (
		projectID = "proj-runtime-first-render"
		issueID   = "az-123"
		worktree  = "/tmp/repo-az-123"
		branch    = "riordan/az-123/task"
	)

	if err := store.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   issueID,
		Path:      worktree,
		Branch:    branch,
		UpdatedAt: time.Date(2026, time.April, 2, 10, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	rawStatus, err := json.Marshal(git.GitStatus{
		Added:          []string{"new.go"},
		Modified:       []string{"changed.go"},
		Staged:         []string{"staged.go"},
		Deleted:        []string{"removed.go"},
		HasChanges:     true,
		GitAdditions:   11,
		GitDeletions:   4,
		GitAheadCount:  2,
		GitBehindCount: 3,
	})
	if err != nil {
		t.Fatalf("marshal git status: %v", err)
	}
	if err := store.UpsertWorktreeStateGitStatus(ctx, projectID, issueID, rawStatus, time.Date(2026, time.April, 2, 10, 1, 0, 0, time.UTC)); err != nil {
		t.Fatalf("seed git status projection: %v", err)
	}

	d := &Daemon{
		cfg:                  Config{Logger: slog.Default()},
		sessionRuntimeStore:  store,
		worktreeRuntimeStore: store,
	}

	now := time.Date(2026, time.April, 2, 10, 2, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID: issueID,
			Session: &domain.Session{
				IssueID:   issueID,
				State:     domain.SessionBusy,
				StartedAt: &now,
			},
		},
		{ID: "az-999"},
	}

	got := d.enrichTasksWithRuntimeProjectionCache(ctx, projectID, tasks)
	if len(got) != 2 {
		t.Fatalf("task count = %d, want 2", len(got))
	}
	if !got[0].HasWorktree {
		t.Fatal("task[0].HasWorktree = false, want true")
	}
	if got[0].Session == nil || got[0].Session.Worktree != worktree {
		t.Fatalf("task[0].Session = %+v, want worktree %s", got[0].Session, worktree)
	}
	if !got[0].HasUncommittedChanges {
		t.Fatal("task[0].HasUncommittedChanges = false, want true")
	}
	if got[0].GitAdditions != 11 || got[0].GitDeletions != 4 {
		t.Fatalf("task[0] git counters = +%d/-%d, want +11/-4", got[0].GitAdditions, got[0].GitDeletions)
	}
	if got[0].GitAheadCount != 2 || got[0].GitBehindCount != 3 {
		t.Fatalf("task[0] ahead/behind = %d/%d, want 2/3", got[0].GitAheadCount, got[0].GitBehindCount)
	}
	if got[1].HasWorktree || got[1].HasUncommittedChanges || got[1].GitAdditions != 0 || got[1].GitDeletions != 0 {
		t.Fatalf("task[1] should remain unchanged, got %+v", got[1])
	}
}

func TestHandleTaskListIsReadOnlyAndUsesProjectionData(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()

	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() {
		if err := issuesClient.CloseDB(); err != nil {
			t.Fatalf("close issues db: %v", err)
		}
	})

	projectID := "proj-read-only"
	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Read-only task list",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	sessionStore := daemonstate.NewStore()
	sessionID := naming.CanonicalSessionID(projectID, taskID)
	if _, err := sessionStore.UpsertSession(projectID, sessionID, taskID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed session store: %v", err)
	}
	sessionSnapshot := sessionStore.ReadSnapshot(projectID)
	storedSession, ok := sessionSnapshot.Sessions[sessionID]
	if !ok {
		t.Fatalf("missing seeded session %q in snapshot", sessionID)
	}

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), logger)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	sessionUpdatedAt := time.Date(2026, time.April, 2, 11, 0, 0, 0, time.UTC)
	if err := runtimeStateStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   taskID,
		State:     daemonstate.SessionStateAttached,
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
			Logger: logger,
		},
		issues:               issuesClient,
		sessionStore:         sessionStore,
		sessionRuntimeStore:  runtimeStateStore,
		worktreeRuntimeStore: runtimeStateStore,
		revision:             map[string]uint64{projectID: 7},
		tmux:                 nil,
		worktree:             &git.WorktreeManager{},
		git:                  &git.Client{},
	}

	resp, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-list",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: projectID},
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
	if got, want := payload.ProjectID, projectID; got != want {
		t.Fatalf("payload.ProjectID = %q, want %q", got, want)
	}
	if got, want := payload.SnapshotRevision, uint64(7); got != want {
		t.Fatalf("payload.SnapshotRevision = %d, want %d", got, want)
	}
	tasks := payload.Tasks
	if got, want := len(tasks), 1; got != want {
		t.Fatalf("task count = %d, want %d", got, want)
	}

	task := tasks[0]
	if task.ID != taskID {
		t.Fatalf("task.ID = %q, want %q", task.ID, taskID)
	}
	if task.Title != "Read-only task list" {
		t.Fatalf("task.Title = %q, want %q", task.Title, "Read-only task list")
	}
	if task.Session == nil {
		t.Fatal("expected task session projection")
	}
	if task.Session.State != domain.SessionBusy {
		t.Fatalf("task.Session.State = %q, want %q", task.Session.State, domain.SessionBusy)
	}
	if task.Session.StartedAt == nil || !task.Session.StartedAt.Equal(storedSession.UpdatedAt.UTC()) {
		t.Fatalf("task.Session.StartedAt = %v, want %v", task.Session.StartedAt, storedSession.UpdatedAt.UTC())
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
		cfg:                  Config{RepoDir: repoDir, Logger: slog.Default()},
		issues:               issuesClient,
		sessionStore:         daemonstate.NewStore(),
		tmux:                 tmux.NewClient(tmuxRunner, slog.Default()),
		sessionRuntimeStore:  runtimeStateStore,
		worktreeRuntimeStore: runtimeStateStore,
	}

	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-list-no-write",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.list",
		Meta:            protocol.Metadata{ProjectID: projectID},
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
		cfg:                  Config{BaseBranch: "main", Logger: slog.Default()},
		git:                  git.NewClient(runner, slog.Default()),
		worktree:             git.NewWorktreeManager(runner, "/tmp/repo-root", slog.Default()),
		worktreeRuntimeStore: store,
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
