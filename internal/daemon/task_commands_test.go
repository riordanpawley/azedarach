package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
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
		Title:    "Read-only task list",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusInProgress,
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
	if session.State != domain.SessionBusy {
		t.Fatalf("session state = %s, want %s", session.State, domain.SessionBusy)
	}
	if session.TotalCount != 1 || session.ActiveCount != 1 || session.PausedCount != 0 {
		t.Fatalf("session counts = %d/%d/%d, want 1/1/0", session.TotalCount, session.ActiveCount, session.PausedCount)
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

	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		sessionStore:           daemonstate.NewStore(),
		runtimeStoresByRoot:    map[string]*daemonstate.RuntimeStateStore{},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{},
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

func TestHandleTaskGetRefreshesOnlyRequestedIssueWorktree(t *testing.T) {
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

	select {
	case result := <-resultCh:
		t.Fatalf("task.get returned before git status refresh completed: %+v", result.resp)
	case <-time.After(100 * time.Millisecond):
	}
	close(statusRelease)

	var result taskGetResult
	select {
	case result = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for task.get result after git status refresh")
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
	if !payload.Tasks[0].HasUncommittedChanges {
		t.Fatalf("response task git state was not refreshed: %+v", payload.Tasks[0])
	}
	if payload.Tasks[0].GitAdditions != 1 {
		t.Fatalf("response git additions = %d, want 1", payload.Tasks[0].GitAdditions)
	}

	select {
	case got := <-statusPaths:
		t.Fatalf("unexpected extra worktree refresh for %q", got)
	case <-time.After(100 * time.Millisecond):
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
		// so it correctly falls back to configured base branch there.
		t.Fatalf("merge-base base ref = %q, want %q", got, want)
	}
}

func TestRefreshWorktreeRuntimeStateMutationDoesNotBypassGitProbeBudget(t *testing.T) {
	ctx := context.WithValue(context.Background(), runtimeReconcileRequestContextKey{}, runtimeReconcileRequestContext{
		Priority: reconcilePriorityManual,
		Reason:   "mutation:session.stop",
	})
	projectID := "proj-refresh-budget"
	statusCalls := 0
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch {
		case len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain":
			return "worktree /tmp/repo-root\nbranch refs/heads/main\n\n" +
				"worktree /tmp/repo-az-1\nbranch refs/heads/az/az-1\n\n" +
				"worktree /tmp/repo-az-2\nbranch refs/heads/az/az-2\n", nil
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
