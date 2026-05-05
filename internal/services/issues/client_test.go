package issues

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_CRUDLifecycle(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	createdID, err := client.Create(ctx, CreateTaskParams{
		Title:       "Create SQLite store client",
		Description: "Implement native Go sqlite path",
		Type:        domain.TypeFeature,
		Priority:    domain.P1,
		Assignee:    "sam",
		Labels:      []string{"cli", "form"},
		Design:      "Use sqlite-backed issue store",
		Notes:       "Initial note",
		Acceptance:  "CRUD works",
		Estimate:    intPtr(5),
	})
	require.NoError(t, err)
	require.NotEmpty(t, createdID)

	tasks, err := client.List(ctx)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, naming.IssueID(createdID), tasks[0].ID)
	assert.Equal(t, "Create SQLite store client", tasks[0].Title)
	assert.Equal(t, "sam", tasks[0].Assignee)
	assert.Equal(t, []string{"cli", "form"}, tasks[0].Labels)
	assert.Equal(t, "Use sqlite-backed issue store", tasks[0].Design)
	assert.Equal(t, "Initial note", tasks[0].Notes)
	assert.Equal(t, "CRUD works", tasks[0].Acceptance)
	require.NotNil(t, tasks[0].Estimate)
	assert.Equal(t, 5, *tasks[0].Estimate)

	searchResults, err := client.Search(ctx, "SQLite")
	require.NoError(t, err)
	require.Len(t, searchResults, 1)
	assert.Equal(t, naming.IssueID(createdID), searchResults[0].ID)

	ready, err := client.Ready(ctx)
	require.NoError(t, err)
	require.Len(t, ready, 1)
	assert.Equal(t, naming.IssueID(createdID), ready[0].ID)

	err = client.Update(ctx, createdID, domain.StatusInProgress)
	require.NoError(t, err)

	err = client.UpdateDetails(ctx, createdID, UpdateTaskParams{
		Title:       "Create native sqlite issue store",
		Description: "No bd shell calls",
		Type:        domain.TypeTask,
		Priority:    domain.P0,
	})
	require.NoError(t, err)

	tasks, err = client.Search(ctx, createdID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, domain.StatusInProgress, tasks[0].Status)
	assert.Equal(t, "Create native sqlite issue store", tasks[0].Title)
	assert.Equal(t, domain.P0, tasks[0].Priority)
	assert.Equal(t, "Initial note", tasks[0].Notes)

	replacementNotes := "Replacement note"
	err = client.UpdateDetails(ctx, createdID, UpdateTaskParams{
		Title:       "Create native sqlite issue store",
		Description: "No bd shell calls",
		Notes:       &replacementNotes,
		Type:        domain.TypeTask,
		Priority:    domain.P0,
	})
	require.NoError(t, err)

	tasks, err = client.Search(ctx, createdID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "Replacement note", tasks[0].Notes)

	clearNotes := ""
	err = client.UpdateDetails(ctx, createdID, UpdateTaskParams{
		Title:       "Create native sqlite issue store",
		Description: "No bd shell calls",
		Notes:       &clearNotes,
		Type:        domain.TypeTask,
		Priority:    domain.P0,
	})
	require.NoError(t, err)

	tasks, err = client.Search(ctx, createdID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "", tasks[0].Notes)

	err = client.Close(ctx, createdID, "done")
	require.NoError(t, err)

	tasks, err = client.Search(ctx, createdID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, domain.StatusDone, tasks[0].Status)

	err = client.Archive(ctx, createdID)
	require.NoError(t, err)

	tasks, err = client.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, tasks)
}

func intPtr(value int) *int {
	return &value
}

func TestClient_ListWithRuntimeReturnsJoinedProjectionFields(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-runtime"

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Runtime joined task",
		Type:     domain.TypeTask,
		Priority: domain.P1,
		Status:   domain.StatusInProgress,
	})
	require.NoError(t, err)

	db, err := sql.Open("sqlite", client.dbPath)
	require.NoError(t, err)
	defer db.Close()

	sessionID := "sess-runtime-1"
	startedAt := time.Date(2026, time.April, 4, 12, 0, 0, 0, time.UTC)
	updatedAt := startedAt.Add(2 * time.Minute)
	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_session_projections (project_id, session_id, issue_id, state, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, projectID, sessionID, taskID, "attached", startedAt.Format(time.RFC3339Nano), updatedAt.Format(time.RFC3339Nano))
	require.NoError(t, err)

	statusRaw, err := json.Marshal(git.GitStatus{
		HasChanges:     true,
		GitAdditions:   7,
		GitDeletions:   3,
		GitAheadCount:  2,
		GitBehindCount: 1,
	})
	require.NoError(t, err)
	worktreePath := "/tmp/proj-runtime-" + taskID
	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_worktree_projections (project_id, issue_id, path, branch, updated_at, git_status_json)
		VALUES (?, ?, ?, ?, ?, ?)
	`, projectID, taskID, worktreePath, "riordan/"+taskID+"/task", updatedAt.Format(time.RFC3339Nano), string(statusRaw))
	require.NoError(t, err)

	tasks, err := client.ListWithRuntime(ctx, projectID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)

	got := tasks[0]
	assert.Equal(t, naming.IssueID(taskID), got.ID)
	require.NotNil(t, got.Session)
	assert.Equal(t, domain.SessionBusy, got.Session.State)
	assert.Equal(t, worktreePath, got.Session.Worktree)
	require.NotNil(t, got.Session.StartedAt)
	assert.True(t, got.Session.StartedAt.Equal(startedAt))
	assert.True(t, got.HasWorktree)
	assert.True(t, got.HasUncommittedChanges)
	assert.Equal(t, 7, got.GitAdditions)
	assert.Equal(t, 3, got.GitDeletions)
	assert.Equal(t, 2, got.GitAheadCount)
	assert.Equal(t, 1, got.GitBehindCount)

	one, err := client.GetWithRuntime(ctx, projectID, taskID)
	require.NoError(t, err)
	assert.Equal(t, got.ID, one.ID)
	require.NotNil(t, one.Session)
	assert.Equal(t, worktreePath, one.Session.Worktree)
	assert.True(t, one.HasWorktree)
	assert.Equal(t, 7, one.GitAdditions)
}

func TestClient_UpdateWithRuntimeReturnsChangedTask(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-update-runtime"

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Runtime update",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	})
	require.NoError(t, err)

	task, err := client.UpdateWithRuntime(ctx, projectID, taskID, domain.StatusInProgress)
	require.NoError(t, err)
	assert.Equal(t, naming.IssueID(taskID), task.ID)
	assert.Equal(t, domain.StatusInProgress, task.Status)

	task, err = client.UpdateDetailsWithRuntime(ctx, projectID, taskID, UpdateTaskParams{
		Title:       "Runtime update changed",
		Description: "Changed through returning API",
		Type:        domain.TypeBug,
		Priority:    domain.P1,
	})
	require.NoError(t, err)
	assert.Equal(t, "Runtime update changed", task.Title)
	assert.Equal(t, domain.TypeBug, task.Type)
	assert.Equal(t, domain.P1, task.Priority)

	runtimeNotes := "Runtime replacement notes"
	task, err = client.UpdateDetailsWithRuntime(ctx, projectID, taskID, UpdateTaskParams{
		Title:       "Runtime update changed",
		Description: "Changed through returning API",
		Notes:       &runtimeNotes,
		Type:        domain.TypeBug,
		Priority:    domain.P1,
	})
	require.NoError(t, err)
	assert.Equal(t, "Runtime replacement notes", task.Notes)
}

func TestClient_AppendNotes(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Attachment notes",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	require.NoError(t, client.AppendNotes(ctx, taskID, "📎 [one.png](.azedarach/images/axu/one.png)"))
	require.NoError(t, client.AppendNotes(ctx, taskID, "📎 [two.png](.azedarach/images/axu/two.png)"))

	db, err := sql.Open("sqlite", client.dbPath)
	require.NoError(t, err)
	defer db.Close()

	var notes string
	err = db.QueryRowContext(ctx, "SELECT COALESCE(notes, '') FROM issues WHERE id = ?", taskID).Scan(&notes)
	require.NoError(t, err)
	assert.Equal(t, "📎 [one.png](.azedarach/images/axu/one.png)\n📎 [two.png](.azedarach/images/axu/two.png)", notes)
}

func TestClient_CreateWithParentDependency(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Parent epic",
		Type:     domain.TypeEpic,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	childID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Child task",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		ParentID: &parentID,
	})
	require.NoError(t, err)

	tasks, err := client.Search(ctx, childID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.NotNil(t, tasks[0].ParentID)
	assert.Equal(t, naming.IssueID(parentID), *tasks[0].ParentID)
}

func TestClient_CreateWithOpenChildReopensClosedParent(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Parent issue",
		Type:     domain.TypeEpic,
		Priority: domain.P2,
	})
	require.NoError(t, err)
	require.NoError(t, client.Update(ctx, parentID, domain.StatusDone))

	_, err = client.Create(ctx, CreateTaskParams{
		Title:    "Open child",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		ParentID: &parentID,
	})
	require.NoError(t, err)

	parentTasks, err := client.Search(ctx, parentID)
	require.NoError(t, err)
	require.Len(t, parentTasks, 1)
	assert.Equal(t, domain.StatusInProgress, parentTasks[0].Status)
}

func TestClient_UpdatePreventsClosingParentWithOpenChildren(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Parent issue",
		Type:     domain.TypeEpic,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	_, err = client.Create(ctx, CreateTaskParams{
		Title:    "Open child",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		ParentID: &parentID,
	})
	require.NoError(t, err)

	err = client.Update(ctx, parentID, domain.StatusDone)
	require.Error(t, err)

	var storeErr *domain.TaskStoreError
	require.ErrorAs(t, err, &storeErr)
	assert.Equal(t, "update", storeErr.Op)
	assert.Equal(t, parentID, storeErr.TaskID)
	assert.ErrorIs(t, storeErr.Err, domain.ErrConflict)

	parentTasks, err := client.Search(ctx, parentID)
	require.NoError(t, err)
	require.Len(t, parentTasks, 1)
	assert.Equal(t, domain.StatusOpen, parentTasks[0].Status)
}

func TestClient_UpdateAllowsClosingParentWhenChildrenAreClosed(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Parent issue",
		Type:     domain.TypeEpic,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	childID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Child issue",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		ParentID: &parentID,
	})
	require.NoError(t, err)

	require.NoError(t, client.Update(ctx, childID, domain.StatusDone))
	require.NoError(t, client.Update(ctx, parentID, domain.StatusDone))

	parentTasks, err := client.Search(ctx, parentID)
	require.NoError(t, err)
	require.Len(t, parentTasks, 1)
	assert.Equal(t, domain.StatusDone, parentTasks[0].Status)
}

func TestClient_AddAndRemoveDependency(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	blockerID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Blocker",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	blockedID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Blocked",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	err = client.AddDependency(ctx, blockedID, blockerID, "blocks")
	require.NoError(t, err)

	tasks, err := client.List(ctx)
	require.NoError(t, err)
	var blockedTask *domain.Task
	for i := range tasks {
		if tasks[i].ID.String() == blockedID {
			blockedTask = &tasks[i]
			break
		}
	}
	if blockedTask == nil {
		t.Fatalf("blocked task %s not found", blockedID)
	}
	require.Len(t, blockedTask.Dependencies, 1)
	assert.Equal(t, blockerID, blockedTask.Dependencies[0].ID.String())
	assert.Equal(t, domain.DependencyBlocks, blockedTask.Dependencies[0].Type)

	err = client.RemoveDependency(ctx, blockedID, blockerID, "blocks")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDependencyRemovalConfirmationRequired)

	err = client.RemoveDependency(WithDependencyRemovalConfirmation(ctx), blockedID, blockerID, "blocks")
	require.NoError(t, err)

	tasks, err = client.List(ctx)
	require.NoError(t, err)
	blockedTask = nil
	for i := range tasks {
		if tasks[i].ID.String() == blockedID {
			blockedTask = &tasks[i]
			break
		}
	}
	if blockedTask == nil {
		t.Fatalf("blocked task %s not found after remove", blockedID)
	}
	assert.Empty(t, blockedTask.Dependencies)
}

func TestClient_RemoveDependencyConfirmationIsNotRequiredForRelatedEdges(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	sourceID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Source",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	relatedID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Related",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	require.NoError(t, client.AddDependency(ctx, relatedID, sourceID, "related"))
	require.NoError(t, client.RemoveDependency(ctx, relatedID, sourceID, "related"))

	tasks, err := client.List(ctx)
	require.NoError(t, err)

	var relatedTask *domain.Task
	for i := range tasks {
		if tasks[i].ID.String() == relatedID {
			relatedTask = &tasks[i]
			break
		}
	}
	if relatedTask == nil {
		t.Fatalf("related task %s not found after remove", relatedID)
	}
	assert.Empty(t, relatedTask.Dependencies)
}

func TestClient_AddDependencyCanonicalizesLegacyAliasesOnNonEpicTasks(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	sourceID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Source",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	blockedID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Blocked",
		Type:     domain.TypeFeature,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	relatedID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Related",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	require.NoError(t, client.AddDependency(ctx, blockedID, sourceID, "blocked-by"))
	require.NoError(t, client.AddDependency(ctx, relatedID, sourceID, "related"))

	tasks, err := client.List(ctx)
	require.NoError(t, err)

	var blockedTask, relatedTask *domain.Task
	for i := range tasks {
		switch tasks[i].ID.String() {
		case blockedID:
			blockedTask = &tasks[i]
		case relatedID:
			relatedTask = &tasks[i]
		}
	}

	if blockedTask == nil {
		t.Fatalf("blocked task %s not found", blockedID)
	}
	if relatedTask == nil {
		t.Fatalf("related task %s not found", relatedID)
	}

	require.Len(t, blockedTask.Dependencies, 1)
	assert.Equal(t, sourceID, blockedTask.Dependencies[0].ID.String())
	assert.Equal(t, domain.DependencyBlocks, blockedTask.Dependencies[0].Type)

	require.Len(t, relatedTask.Dependencies, 1)
	assert.Equal(t, sourceID, relatedTask.Dependencies[0].ID.String())
	assert.Equal(t, domain.DependencyRelatedTo, relatedTask.Dependencies[0].Type)
}

func TestClient_AddDependencyPreventsDuplicateEdges(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	sourceID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Source",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	blockedID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Blocked",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	require.NoError(t, client.AddDependency(ctx, blockedID, sourceID, "blocks"))
	require.NoError(t, client.AddDependency(ctx, blockedID, sourceID, "blocks"))

	tasks, err := client.List(ctx)
	require.NoError(t, err)

	var blockedTask *domain.Task
	for i := range tasks {
		if tasks[i].ID.String() == blockedID {
			blockedTask = &tasks[i]
			break
		}
	}
	if blockedTask == nil {
		t.Fatalf("blocked task %s not found", blockedID)
	}

	require.Len(t, blockedTask.Dependencies, 1)
	assert.Equal(t, sourceID, blockedTask.Dependencies[0].ID.String())
	assert.Equal(t, domain.DependencyBlocks, blockedTask.Dependencies[0].Type)
}

func TestClient_AddParentChildDependencyReopensClosedParentForOpenChild(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Parent",
		Type:     domain.TypeEpic,
		Priority: domain.P2,
	})
	require.NoError(t, err)
	require.NoError(t, client.Update(ctx, parentID, domain.StatusDone))

	childID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Child",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	require.NoError(t, client.AddDependency(ctx, childID, parentID, "parent-child"))

	parentTasks, err := client.Search(ctx, parentID)
	require.NoError(t, err)
	require.Len(t, parentTasks, 1)
	assert.Equal(t, domain.StatusInProgress, parentTasks[0].Status)
}

func TestClient_AddParentChildDependencyKeepsClosedParentWhenChildClosed(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Parent",
		Type:     domain.TypeEpic,
		Priority: domain.P2,
	})
	require.NoError(t, err)
	require.NoError(t, client.Update(ctx, parentID, domain.StatusDone))

	childID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Child",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)
	require.NoError(t, client.Update(ctx, childID, domain.StatusDone))

	require.NoError(t, client.AddDependency(ctx, childID, parentID, "parent-child"))

	parentTasks, err := client.Search(ctx, parentID)
	require.NoError(t, err)
	require.Len(t, parentTasks, 1)
	assert.Equal(t, domain.StatusDone, parentTasks[0].Status)
}

func TestClient_ListHydratesParentChildAfterTaskSliceGrowth(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	parentID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Hydration parent",
		Type:     domain.TypeEpic,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	// Create enough tasks to exceed the initial query slice capacity (32),
	// which previously exposed pointer invalidation when hydrating deps.
	var earlyChildID string
	for i := 0; i < 40; i++ {
		title := "Hydration task " + strconv.Itoa(i)
		params := CreateTaskParams{
			Title:    title,
			Type:     domain.TypeTask,
			Priority: domain.P2,
		}
		if i == 0 {
			params.Title = "Hydration early child"
			params.ParentID = &parentID
		}
		id, createErr := client.Create(ctx, params)
		require.NoError(t, createErr)
		if i == 0 {
			earlyChildID = id
		}
	}

	tasks, err := client.List(ctx)
	require.NoError(t, err)

	var earlyChild *domain.Task
	for i := range tasks {
		if tasks[i].ID.String() == earlyChildID {
			earlyChild = &tasks[i]
			break
		}
	}
	if earlyChild == nil {
		t.Fatalf("early child task %s not found", earlyChildID)
	}
	require.NotNil(t, earlyChild.ParentID)
	assert.Equal(t, parentID, earlyChild.ParentID.String())
}

func TestClient_AddDependencyRequiresExistingTargetIssue(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	sourceID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Source",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	err = client.AddDependency(ctx, sourceID, "missing-target", "blocks")
	require.Error(t, err)

	var storeErr *domain.TaskStoreError
	require.ErrorAs(t, err, &storeErr)
	assert.Equal(t, "add-dependency", storeErr.Op)
	assert.Equal(t, sourceID, storeErr.TaskID)
	assert.ErrorIs(t, storeErr.Err, domain.ErrNotFound)

	tasks, err := client.List(ctx)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Empty(t, tasks[0].Dependencies)
}

func TestClient_AddDependencyRejectsCycle(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	aID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Task A",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	bID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Task B",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	cID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Task C",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	require.NoError(t, client.AddDependency(ctx, bID, aID, "blocks"))
	require.NoError(t, client.AddDependency(ctx, cID, bID, "blocks"))

	err = client.AddDependency(ctx, aID, cID, "blocks")
	require.Error(t, err)

	var storeErr *domain.TaskStoreError
	require.ErrorAs(t, err, &storeErr)
	assert.Equal(t, "add-dependency", storeErr.Op)
	assert.ErrorIs(t, storeErr.Err, domain.ErrConflict)
}

func TestClient_DeleteRemovesIssue(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Delete me",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)

	err = client.Delete(ctx, taskID)
	require.NoError(t, err)

	tasks, err := client.Search(ctx, taskID)
	require.NoError(t, err)
	assert.Empty(t, tasks)
}

func TestClient_DeleteBlockedWhenTaskHasWorktreeProjection(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-delete-worktree"

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Delete blocked by worktree",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)

	db, err := sql.Open("sqlite", "file:"+client.dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_worktree_projections (project_id, issue_id, path, branch, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`, projectID, taskID, "/tmp/"+taskID, "riordan/"+taskID, time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)

	err = client.Delete(ctx, taskID)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDeleteBlockedByRuntimeAttachments)

	tasks, findErr := client.Search(ctx, taskID)
	require.NoError(t, findErr)
	require.Len(t, tasks, 1)
}

func TestClient_DeleteBlockedWhenTaskHasActiveSessionProjection(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-delete-session"

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Delete blocked by active session",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)

	db, err := sql.Open("sqlite", "file:"+client.dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_session_projections (project_id, session_id, issue_id, state, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, projectID, "sess-"+taskID, taskID, "attached", time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)

	err = client.Delete(ctx, taskID)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDeleteBlockedByRuntimeAttachments)

	tasks, findErr := client.Search(ctx, taskID)
	require.NoError(t, findErr)
	require.Len(t, tasks, 1)
}

func TestClient_DeleteAllowsStoppedSessionWithoutWorktreeProjection(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	const projectID = "proj-delete-stopped"

	taskID, err := client.Create(ctx, CreateTaskParams{
		Title:    "Delete allowed with stopped session",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)

	db, err := sql.Open("sqlite", "file:"+client.dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.ExecContext(ctx, `
		INSERT INTO daemon_session_projections (project_id, session_id, issue_id, state, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, projectID, "sess-"+taskID, taskID, "stopped", "", time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)

	require.NoError(t, client.Delete(ctx, taskID))

	tasks, findErr := client.Search(ctx, taskID)
	require.NoError(t, findErr)
	assert.Empty(t, tasks)
}

func TestClient_CreateDoesNotReuseDeletedLocalIssueIDs(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	firstID, err := client.Create(ctx, CreateTaskParams{Title: "first", Type: domain.TypeTask, Priority: domain.P3})
	require.NoError(t, err)
	secondID, err := client.Create(ctx, CreateTaskParams{Title: "second", Type: domain.TypeTask, Priority: domain.P3})
	require.NoError(t, err)
	thirdID, err := client.Create(ctx, CreateTaskParams{Title: "third", Type: domain.TypeTask, Priority: domain.P3})
	require.NoError(t, err)
	require.Equal(t, "a", firstID)
	require.Equal(t, "b", secondID)
	require.Equal(t, "c", thirdID)

	require.NoError(t, client.Delete(ctx, secondID))

	fourthID, err := client.Create(ctx, CreateTaskParams{Title: "fourth", Type: domain.TypeTask, Priority: domain.P3})
	require.NoError(t, err)
	assert.Equal(t, "d", fourthID)
}

func TestClient_CreateSkipsHistoricallyUsedIDsWhenMetaIndexDrifts(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	firstID, err := client.Create(ctx, CreateTaskParams{Title: "first", Type: domain.TypeTask, Priority: domain.P3})
	require.NoError(t, err)
	secondID, err := client.Create(ctx, CreateTaskParams{Title: "second", Type: domain.TypeTask, Priority: domain.P3})
	require.NoError(t, err)
	require.Equal(t, "a", firstID)
	require.Equal(t, "b", secondID)

	// Simulate metadata/index drift pointing at an already-used historical ID.
	db, err := sql.Open("sqlite", "file:"+client.dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`UPDATE meta SET value = '0' WHERE key = ?`, nextAlphaIssueIndexMetaKey)
	require.NoError(t, err)

	thirdID, err := client.Create(ctx, CreateTaskParams{Title: "third", Type: domain.TypeTask, Priority: domain.P3})
	require.NoError(t, err)
	assert.Equal(t, "c", thirdID)
}

func TestClient_ErrorWrapping(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	err := client.Update(ctx, "does-not-exist", domain.StatusDone)
	require.Error(t, err)

	var storeErr *domain.TaskStoreError
	require.ErrorAs(t, err, &storeErr)
	assert.Equal(t, "update", storeErr.Op)
	assert.Equal(t, "does-not-exist", storeErr.TaskID)
}

func TestClient_DBHandleReusedUntilExplicitClose(t *testing.T) {
	client := newTestClient(t)

	first, err := client.dbHandle()
	require.NoError(t, err)

	second, err := client.dbHandle()
	require.NoError(t, err)
	assert.Same(t, first, second)

	require.NoError(t, client.CloseDB())

	third, err := client.dbHandle()
	require.NoError(t, err)
	assert.NotSame(t, first, third)
}

func TestClient_DBHandleCreatesMissingParentDirectory(t *testing.T) {
	base := t.TempDir()
	dbPath := filepath.Join(base, "missing", "nested", "azedarach.db")
	client := NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() {
		require.NoError(t, client.CloseDB())
	})

	_, err := client.dbHandle()
	require.NoError(t, err)

	_, statErr := os.Stat(filepath.Dir(dbPath))
	require.NoError(t, statErr)
}

func TestClient_ConfiguresSQLitePragmas(t *testing.T) {
	client := newTestClient(t)
	db, err := client.dbHandle()
	require.NoError(t, err)

	var foreignKeys int
	require.NoError(t, db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys))
	assert.Equal(t, 1, foreignKeys)

	var journalMode string
	require.NoError(t, db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode))
	assert.Equal(t, "wal", strings.ToLower(journalMode))
}

func TestClient_EnsuresDependencyForeignKeysAndIndexes(t *testing.T) {
	client := newTestClient(t)
	db, err := client.dbHandle()
	require.NoError(t, err)

	type fkRow struct {
		table string
		from  string
		to    string
	}
	fkRows, err := db.Query(`PRAGMA foreign_key_list('issue_dependencies')`)
	require.NoError(t, err)
	defer fkRows.Close()

	fks := make([]fkRow, 0, 2)
	for fkRows.Next() {
		var (
			id       int
			seq      int
			table    string
			from     string
			to       string
			onUpdate string
			onDelete string
			match    string
		)
		require.NoError(t, fkRows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match))
		fks = append(fks, fkRow{table: table, from: from, to: to})
	}
	require.NoError(t, fkRows.Err())
	assert.Contains(t, fks, fkRow{table: "issues", from: "issue_id", to: "id"})
	assert.Contains(t, fks, fkRow{table: "issues", from: "depends_on_id", to: "id"})

	wantIndexes := []string{
		"idx_issues_deleted_updated",
		"idx_issues_status_deleted_priority_updated",
		"idx_dependencies_issue_active_type",
		"idx_dependencies_depends_on_active_type",
		"idx_dependencies_depends_on",
	}
	indexRows, err := db.Query(`
		SELECT name
		FROM sqlite_master
		WHERE type = 'index' AND name IN (?, ?, ?, ?, ?)
		ORDER BY name
	`, wantIndexes[0], wantIndexes[1], wantIndexes[2], wantIndexes[3], wantIndexes[4])
	require.NoError(t, err)
	defer indexRows.Close()

	gotIndexes := make([]string, 0, len(wantIndexes))
	for indexRows.Next() {
		var name string
		require.NoError(t, indexRows.Scan(&name))
		gotIndexes = append(gotIndexes, name)
	}
	require.NoError(t, indexRows.Err())
	assert.ElementsMatch(t, wantIndexes, gotIndexes)

	_, err = db.Exec(`
		INSERT INTO issue_dependencies (issue_id, depends_on_id, dependency_type, tombstoned_at)
		VALUES ('missing-source', 'missing-target', 'blocks', NULL)
	`)
	require.Error(t, err)
}

func TestClient_MigratesLegacySchemaShape(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")

	db, err := sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS issues (
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
		);
		CREATE TABLE IF NOT EXISTS issue_dependencies (
			issue_id TEXT NOT NULL,
			depends_on_id TEXT NOT NULL,
			dependency_type TEXT NOT NULL,
			tombstoned_at TEXT,
			PRIMARY KEY (issue_id, depends_on_id, dependency_type)
		);
		CREATE TABLE IF NOT EXISTS meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_dependencies_depends_on
			ON issue_dependencies(depends_on_id, dependency_type, tombstoned_at);
	`)
	require.NoError(t, err)

	client := NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() {
		require.NoError(t, client.CloseDB())
	})

	_, err = client.Create(ctx, CreateTaskParams{
		Title:    "legacy schema compatibility",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)

	rows, err := db.Query(`SELECT id FROM schema_migrations ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		got = append(got, id)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{
		"0001_bootstrap_tables",
		"0002_dependency_foreign_keys",
		"0003_issue_indexes",
		"0004_spec_tables",
		"0005_spec_audit_log",
		"0006_external_issue_sync",
	}, got)
}

func TestClient_CreateWaitsForWriteLockAndSucceedsWithinBusyTimeout(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	_, err := client.Create(ctx, CreateTaskParams{
		Title:    "warmup",
		Type:     domain.TypeTask,
		Priority: domain.P3,
	})
	require.NoError(t, err)

	lockDB, err := sql.Open("sqlite", "file:"+client.dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lockDB.Close() })

	_, err = lockDB.Exec(`BEGIN IMMEDIATE`)
	require.NoError(t, err)

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, createErr := client.Create(ctx, CreateTaskParams{
			Title:    "wait-for-lock",
			Type:     domain.TypeTask,
			Priority: domain.P3,
		})
		done <- createErr
	}()

	time.Sleep(250 * time.Millisecond)
	_, err = lockDB.Exec(`COMMIT`)
	require.NoError(t, err)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("create did not complete after releasing write lock")
	}

	assert.GreaterOrEqual(t, time.Since(start), 200*time.Millisecond)
}

func newTestClient(t *testing.T) *Client {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

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
		_, err := db.Exec(stmt)
		require.NoError(t, err)
	}

	client := NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() {
		require.NoError(t, client.CloseDB())
	})
	return client
}

func TestResolveDBPathUsesEnvOverride(t *testing.T) {
	t.Setenv("AZEDARACH_DB_PATH", "/tmp/custom-azedarach.db")
	got, err := resolveDBPath(t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, "/tmp/custom-azedarach.db", got)
}

func TestResolveDBPathUsesBaseRepoForWorktree(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	start := filepath.Join(worktree, "go-bubbletea")

	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755))
	require.NoError(t, os.MkdirAll(start, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644))

	t.Setenv("PATH", "")
	got, err := resolveDBPath(start)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(repo, ".azedarach", "azedarach.db"), got)
}

func TestResolveDBPathFallsBackToGitMarkerWhenGitUnavailable(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	start := filepath.Join(worktree, "go-bubbletea")

	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755))
	require.NoError(t, os.MkdirAll(start, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644))

	t.Setenv("PATH", "")
	got, err := resolveDBPath(start)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(repo, ".azedarach", "azedarach.db"), got)
}
