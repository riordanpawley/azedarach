package issues

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/riordanpawley/azedarach/internal/domain"
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
	})
	require.NoError(t, err)
	require.NotEmpty(t, createdID)

	tasks, err := client.List(ctx)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, createdID, tasks[0].ID)
	assert.Equal(t, "Create SQLite store client", tasks[0].Title)

	searchResults, err := client.Search(ctx, "SQLite")
	require.NoError(t, err)
	require.Len(t, searchResults, 1)
	assert.Equal(t, createdID, searchResults[0].ID)

	ready, err := client.Ready(ctx)
	require.NoError(t, err)
	require.Len(t, ready, 1)
	assert.Equal(t, createdID, ready[0].ID)

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
	assert.Equal(t, parentID, *tasks[0].ParentID)
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
		if tasks[i].ID == blockedID {
			blockedTask = &tasks[i]
			break
		}
	}
	if blockedTask == nil {
		t.Fatalf("blocked task %s not found", blockedID)
	}
	require.Len(t, blockedTask.Dependencies, 1)
	assert.Equal(t, blockerID, blockedTask.Dependencies[0].ID)
	assert.Equal(t, domain.DependencyBlocks, blockedTask.Dependencies[0].Type)

	err = client.RemoveDependency(ctx, blockedID, blockerID, "blocks")
	require.NoError(t, err)

	tasks, err = client.List(ctx)
	require.NoError(t, err)
	blockedTask = nil
	for i := range tasks {
		if tasks[i].ID == blockedID {
			blockedTask = &tasks[i]
			break
		}
	}
	if blockedTask == nil {
		t.Fatalf("blocked task %s not found after remove", blockedID)
	}
	assert.Empty(t, blockedTask.Dependencies)
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

	return NewClientAtPath(dbPath, slog.Default())
}

func TestResolveDBPathUsesEnvOverride(t *testing.T) {
	t.Setenv("AZEDARACH_DB_PATH", "/tmp/custom-azedarach.db")
	got, err := resolveDBPath(t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, "/tmp/custom-azedarach.db", got)
}

func TestResolveBaseGitRootAbsoluteCommonDir(t *testing.T) {
	start := filepath.Join(t.TempDir(), "repo", "worktree-a")
	got, err := baseGitRootFromCommonDir(start, "/tmp/repo/.git")
	require.NoError(t, err)
	assert.Equal(t, "/tmp/repo", got)
}

func TestResolveBaseGitRootRelativeCommonDir(t *testing.T) {
	start := filepath.Join(t.TempDir(), "repo", "worktree-a")
	got, err := baseGitRootFromCommonDir(start, "../.git")
	require.NoError(t, err)
	assert.Equal(t, filepath.Clean(filepath.Join(start, "..")), got)
}

func TestResolveBaseGitRootRejectsNonDotGitPath(t *testing.T) {
	start := t.TempDir()
	_, err := baseGitRootFromCommonDir(start, "/tmp/repo/gitdir")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected .git path")
}

func TestResolveBaseGitRootNestedWorktreeCommonDir(t *testing.T) {
	start := filepath.Join(t.TempDir(), "repo", "worktree-a")
	got, err := baseGitRootFromCommonDir(start, "/tmp/repo/.git/worktrees/worktree-a")
	require.NoError(t, err)
	assert.Equal(t, "/tmp/repo", got)
}

func TestResolveDBPathFromBaseGitRoot(t *testing.T) {
	base := t.TempDir()
	worktree := filepath.Join(base, "worktree-a")
	require.NoError(t, os.MkdirAll(worktree, 0o755))

	got, err := resolveDBPathFromGitCommonDir(worktree, "../.git")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(base, ".azedarach", "azedarach.db"), got)
}

func TestResolveDBPathBySearchFindsParentStore(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(repo, "worktree-a")
	storeDir := filepath.Join(repo, ".azedarach")
	require.NoError(t, os.MkdirAll(worktree, 0o755))
	require.NoError(t, os.MkdirAll(storeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(storeDir, "azedarach.db"), []byte(""), 0o644))

	got, err := resolveDBPathBySearch(worktree)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(storeDir, "azedarach.db"), got)
}

func TestParseGitDirPointer(t *testing.T) {
	got, err := parseGitDirPointer("gitdir: /tmp/repo/.git/worktrees/wt\n")
	require.NoError(t, err)
	assert.Equal(t, "/tmp/repo/.git/worktrees/wt", got)
}

func TestResolveBaseGitRootFromGitMarkerWorktreePointer(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	start := filepath.Join(worktree, "go-bubbletea")

	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755))
	require.NoError(t, os.MkdirAll(start, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644))

	got, err := resolveBaseGitRootFromGitMarker(start)
	require.NoError(t, err)
	assert.Equal(t, repo, got)
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
