package daemon

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
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

func TestHandleTaskListFreshRuntimeCacheHitSkipsRuntimeRefreshTriggers(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-cache-list"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "cached list",
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
		Title:    "cached list",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
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

	d.worktreeStateRefreshMu.Lock()
	defer d.worktreeStateRefreshMu.Unlock()
	if got := d.worktreeStateLastRefresh[projectID]; !got.IsZero() {
		t.Fatalf("worktree refresh was triggered at %v on cache hit", got)
	}
	if d.worktreeStateRefreshing[projectID] {
		t.Fatal("worktree refresh marked in-flight on cache hit")
	}
}

func TestHandleTaskListStaleRuntimeCacheRebuildsAndRefreshes(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	projectID := "proj-cache-list-runtime-stale"
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(issuesDBPath, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	taskID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "runtime stale cache",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	now := time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC)
	originalNow := timeNow
	t.Cleanup(func() { timeNow = originalNow })
	timeNow = func() time.Time { return now }

	cachedTask := domain.Task{
		ID:       naming.IssueID(taskID),
		Title:    "stale cached title",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
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
		taskListSnapshotCache: map[string]taskListSnapshotCacheEntry{
			projectID: {
				Revision:      11,
				LastCheckedAt: now,
				Freshness:     protocol.TaskListFreshnessFresh,
				Tasks:         []domain.Task{cachedTask},
				CachedAt:      now,
				RuntimeAt:     now.Add(-taskListSnapshotRuntimeCacheTTL - time.Millisecond),
				SummariesOnly: true,
			},
		},
	}

	resp, err := d.handleTaskList(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-task-list-runtime-cache-stale",
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
	if got, want := payload.Tasks[0].Title, "runtime stale cache"; got != want {
		t.Fatalf("payload task title = %q, want refreshed %q", got, want)
	}

	d.worktreeStateRefreshMu.Lock()
	gotRefresh := d.worktreeStateLastRefresh[projectID]
	d.worktreeStateRefreshMu.Unlock()
	if gotRefresh.IsZero() {
		t.Fatal("worktree refresh was not triggered after runtime cache staled")
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
		}, projectID)
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
	}, projectID)
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

func taskClosePhaseNames(phases []taskClosePhaseTiming) []string {
	names := make([]string, 0, len(phases))
	for _, phase := range phases {
		names = append(names, phase.Name)
	}
	return names
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
		ID:            sessionID,
		IssueID:       taskID,
		State:         daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateAttached,
		UpdatedAt:     time.Now().UTC(),
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
		case len(args) >= 5 && args[0] == "-C" && args[2] == "rev-list":
			return "0", nil
		case len(args) >= 6 && args[0] == "-C" && args[2] == "merge-tree":
			return "tree-sha", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "fetch":
			return "", nil
		case len(args) >= 5 && args[0] == "-C" && args[2] == "checkout":
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

func TestTaskCompletionAdviceIncludesDomainNextSteps(t *testing.T) {
	advice := daemonTaskCompletionAdvice("az-1", []string{"az-2"}, []string{"az-3"}, []string{"az-4"})
	joined := strings.Join(advice, "\n")
	for _, want := range []string{
		"az orchestrate close-session --issue az-4",
		"az issue close --id az-3",
		"az orchestrate start --root az-1 --issue az-2 --json",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("advice = %+v, missing %q", advice, want)
		}
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

func TestHandleTaskGetTriggersOnlyRequestedIssueWorktreeRefreshAsync(t *testing.T) {
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
