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

func TestStopUserProjectionWorkersWaitsAndRejectsNewWork(t *testing.T) {
	d := &Daemon{
		userStoreRefreshPending: map[string]bool{},
		userStoreRefreshDirty:   map[string]bool{},
	}
	workerStarted := make(chan struct{})
	releaseWorker := make(chan struct{})
	d.userStoreRefreshMu.Lock()
	d.userStoreRefreshWG.Add(1)
	d.userStoreRefreshMu.Unlock()
	go func() {
		defer d.userStoreRefreshWG.Done()
		close(workerStarted)
		<-releaseWorker
	}()
	<-workerStarted
	stopped := make(chan struct{})
	go func() {
		d.stopUserProjectionWorkers()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("projection shutdown returned before active worker completed")
	case <-time.After(20 * time.Millisecond):
	}
	d.userStoreRefreshMu.Lock()
	if !d.userStoreRefreshStopping {
		d.userStoreRefreshMu.Unlock()
		t.Fatal("projection shutdown did not reject new workers")
	}
	d.userStoreRefreshMu.Unlock()
	close(releaseWorker)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("projection shutdown did not finish after active worker completed")
	}
}

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
