package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/userstore"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestGlobalSnapshotScopeExcludesUnavailableProjectFromPartialHealth(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p1, p2 := t.TempDir(), t.TempDir()
	if err := appconfig.SaveProjectsRegistry(&appconfig.ProjectsRegistry{Projects: []appconfig.Project{{ID: "p1", Name: "P1", Path: p1}, {ID: "p2", Name: "P2", Path: p2}}}); err != nil {
		t.Fatal(err)
	}
	store, err := userstore.Open(filepath.Join(t.TempDir(), "user.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	if err = store.ReplaceProject(ctx, userstore.ProjectInput{ProjectID: "p1", Name: "P1", Path: p1, DBPath: filepath.Join(p1, ".azedarach", "azedarach.db"), Tasks: []domain.Task{{ID: "one", Title: "One", Status: domain.StatusOpen, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now}}}); err != nil {
		t.Fatal(err)
	}
	if err = store.MarkUnavailable(ctx, "p2", "P2", p2, filepath.Join(p2, ".azedarach", "azedarach.db"), errors.New("offline")); err != nil {
		t.Fatal(err)
	}
	view := domain.DefaultBoardView()
	view.ID = "selected-only"
	if _, err = store.SaveGlobalView(ctx, protocol.GlobalViewRecord{View: view, Scope: protocol.GlobalViewScope{Kind: protocol.GlobalViewScopeSelectedProjects, ProjectIDs: []naming.ProjectID{"p1"}}}); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{userStore: store}
	raw, _ := json.Marshal(protocol.GlobalSnapshotRequestBody{ViewID: "selected-only"})
	resp, err := d.handleGlobalSnapshot(ctx, protocol.RequestEnvelope{Body: raw})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("response error: %+v", resp.Error)
	}
	var snapshot protocol.GlobalSnapshotResponseBody
	if err = json.Unmarshal(resp.Body, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Partial || len(snapshot.Projects) != 1 || snapshot.Projects[0].ProjectID != "p1" {
		t.Fatalf("scoped snapshot = %+v, want only healthy p1", snapshot)
	}
}

func TestGlobalSnapshotDoesNotReconcileProjectCatalog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	if err := appconfig.SaveProjectsRegistry(&appconfig.ProjectsRegistry{Projects: []appconfig.Project{{ID: "p1", Name: "P1", Path: root}}}); err != nil {
		t.Fatal(err)
	}
	store, err := userstore.Open(filepath.Join(t.TempDir(), "user.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err = store.ReplaceProject(context.Background(), userstore.ProjectInput{ProjectID: "p1", Name: "P1", Path: root, DBPath: filepath.Join(root, ".azedarach", "azedarach.db")}); err != nil {
		t.Fatal(err)
	}
	if err = appconfig.SaveProjectsRegistry(&appconfig.ProjectsRegistry{}); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{userStore: store}
	resp, err := d.handleGlobalSnapshot(context.Background(), protocol.RequestEnvelope{})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("response error: %+v", resp.Error)
	}
	snapshot, err := store.Snapshot(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Projects) != 1 || !snapshot.Projects[0].Registered {
		t.Fatalf("global snapshot mutated catalog registration: %+v", snapshot.Projects)
	}
}

func TestTmuxSelectorGlobalSnapshotReturnsCachedValueWhileRefreshing(t *testing.T) {
	store, projectID := openSelectorSnapshotTestStore(t, "first")
	d := &Daemon{cfg: Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, userStore: store}
	t.Cleanup(d.stopUserProjectionWorkers)
	ctx := context.Background()
	body := protocol.GlobalSnapshotRequestBody{Consumer: protocol.GlobalViewConsumerTmuxSelector}

	first := readGlobalSnapshotResponse(t, d, ctx, body)
	if got := first.Projects[0].Tasks[0].Title; got != "first" {
		t.Fatalf("cold snapshot title = %q, want first", got)
	}
	if err := store.ReplaceProject(ctx, userstore.ProjectInput{
		ProjectID: projectID,
		Name:      "Project",
		Path:      first.Projects[0].Path,
		DBPath:    first.Projects[0].DBPath,
		Tasks:     []domain.Task{{ID: "one", Title: "second", Status: domain.StatusInProgress, Type: domain.TypeTask, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}},
	}); err != nil {
		t.Fatal(err)
	}

	warm := readGlobalSnapshotResponse(t, d, ctx, body)
	if got := warm.Projects[0].Tasks[0].Title; got != "first" {
		t.Fatalf("warm snapshot title = %q, want cached first", got)
	}
	key, err := selectorSnapshotRequestKey(body)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		cached, ok := d.selectorSnapshots.get(key)
		var snapshot protocol.GlobalSnapshotResponseBody
		if ok && json.Unmarshal(cached, &snapshot) == nil && len(snapshot.Projects) == 1 && snapshot.Projects[0].Tasks[0].Title == "second" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("selector snapshot cache did not refresh to second")
		}
		time.Sleep(time.Millisecond)
	}
	refreshed := readGlobalSnapshotResponse(t, d, ctx, body)
	if got := refreshed.Projects[0].Tasks[0].Title; got != "second" {
		t.Fatalf("refreshed snapshot title = %q, want second", got)
	}
}

func TestNonSelectorGlobalSnapshotKeepsStrongReadBehavior(t *testing.T) {
	store, projectID := openSelectorSnapshotTestStore(t, "first")
	d := &Daemon{userStore: store}
	ctx := context.Background()
	body := protocol.GlobalSnapshotRequestBody{Consumer: protocol.GlobalViewConsumerBoard}
	_ = readGlobalSnapshotResponse(t, d, ctx, body)
	snapshot, err := store.Snapshot(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ReplaceProject(ctx, userstore.ProjectInput{
		ProjectID: projectID,
		Name:      "Project",
		Path:      snapshot.Projects[0].Path,
		DBPath:    snapshot.Projects[0].DBPath,
		Tasks:     []domain.Task{{ID: "one", Title: "second", Status: domain.StatusInProgress, Type: domain.TypeTask, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}},
	}); err != nil {
		t.Fatal(err)
	}
	second := readGlobalSnapshotResponse(t, d, ctx, body)
	if got := second.Projects[0].Tasks[0].Title; got != "second" {
		t.Fatalf("second strong-read title = %q, want second", got)
	}
}

func openSelectorSnapshotTestStore(t *testing.T, title string) (*userstore.Store, string) {
	t.Helper()
	store, err := userstore.Open(filepath.Join(t.TempDir(), "user.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	root := t.TempDir()
	projectID := "project"
	now := time.Now().UTC()
	if err = store.ReplaceProject(context.Background(), userstore.ProjectInput{
		ProjectID: projectID,
		Name:      "Project",
		Path:      root,
		DBPath:    filepath.Join(root, ".azedarach", "azedarach.db"),
		Tasks:     []domain.Task{{ID: "one", Title: title, Status: domain.StatusInProgress, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now}},
	}); err != nil {
		t.Fatal(err)
	}
	return store, projectID
}

func readGlobalSnapshotResponse(t *testing.T, d *Daemon, ctx context.Context, body protocol.GlobalSnapshotRequestBody) protocol.GlobalSnapshotResponseBody {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := d.handleGlobalSnapshot(ctx, protocol.RequestEnvelope{Body: raw})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("global snapshot response error: %+v", resp.Error)
	}
	var snapshot protocol.GlobalSnapshotResponseBody
	if err = json.Unmarshal(resp.Body, &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestScopedRuntimeNeverOpensOrCreatesUserProjectionDatabase(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")

	d := New(Config{
		RepoDir:       repo,
		ScopedRuntime: true,
		SocketPath:    filepath.Join(t.TempDir(), "azd.sock"),
		LockPath:      filepath.Join(t.TempDir(), "azd.lock"),
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(d.closeRuntimeStateStores)

	if d.userStore != nil {
		t.Fatal("scoped runtime opened the user cross-project projection")
	}
	userDB, err := appconfig.UserDBPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(userDB); !os.IsNotExist(err) {
		t.Fatalf("scoped runtime touched user database %q: %v", userDB, err)
	}
	if _, err = os.Stat(filepath.Join(repo, ".azedarach", "azedarach.db")); err != nil {
		t.Fatalf("scoped runtime database missing: %v", err)
	}
}
