package daemon

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/userstore"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

func TestUserProjectionConsumerReplaysAndWatchesWithoutMutationFullExport(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	projectID, err := appconfig.ProjectIDForRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	project := appconfig.Project{ID: projectID, Name: "P", Path: root}
	if err := appconfig.SaveProjectsRegistry(&appconfig.ProjectsRegistry{Projects: []appconfig.Project{project}}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := issues.NewClientAtPath(filepath.Join(root, ".azedarach", "azedarach.db"), logger)
	if err := client.OpenProjectionDeltaStore(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.CloseDB() })
	store, err := userstore.Open(filepath.Join(home, ".azedarach", "azedarach.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	d := &Daemon{
		cfg:                     Config{RepoDir: root, Logger: logger},
		issues:                  client,
		issueClientsByProject:   map[string]*issues.Client{projectID: client},
		issueClientsByRoot:      map[string]*issues.Client{daemonStoreRootKey(root): client},
		userStore:               store,
		userStoreRefreshPending: map[string]bool{},
		userStoreRefreshDirty:   map[string]bool{},
		userStoreRefreshActive:  map[string]bool{},
		userProjectionConsumers: map[string]*userProjectionConsumerHandle{},
	}
	if err := d.refreshRegisteredUserProject(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	var fullRefreshes atomic.Int32
	d.userStoreProjectLockHook = func(string, bool) { fullRefreshes.Add(1) }

	issueID, err := client.Create(context.Background(), issues.CreateTaskParams{Title: "first", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.ensureUserProjectionConsumers(ctx, []appconfig.Project{project})
	waitForGlobalProjectedTask(t, store, projectID, issueID, domain.StatusOpen, 1)
	if err := client.Update(context.Background(), issueID, domain.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	waitForGlobalProjectedTask(t, store, projectID, issueID, domain.StatusInProgress, 2)
	if got := fullRefreshes.Load(); got != 0 {
		t.Fatalf("normal delta delivery ran full export critical section %d times", got)
	}
	cancel()
	d.stopUserProjectionWorkers()

	// A daemon restart resumes from the durable root cursor. A mutation committed
	// while no consumer is running is replayed without a startup full export.
	if err := client.Update(context.Background(), issueID, domain.StatusInReview); err != nil {
		t.Fatal(err)
	}
	d2 := &Daemon{
		cfg:                     Config{RepoDir: root, Logger: logger},
		issues:                  client,
		issueClientsByProject:   map[string]*issues.Client{projectID: client},
		issueClientsByRoot:      map[string]*issues.Client{daemonStoreRootKey(root): client},
		userStore:               store,
		userProjectionConsumers: map[string]*userProjectionConsumerHandle{},
	}
	var restartFullRefreshes atomic.Int32
	d2.userStoreProjectLockHook = func(string, bool) { restartFullRefreshes.Add(1) }
	if err := d2.bootstrapUserProjection(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := restartFullRefreshes.Load(); got != 0 {
		t.Fatalf("restart bootstrap replaced initialized project %d times", got)
	}
	restartCtx, restartCancel := context.WithCancel(context.Background())
	d2.ensureUserProjectionConsumers(restartCtx, []appconfig.Project{project})
	waitForGlobalProjectedTask(t, store, projectID, issueID, domain.StatusInReview, 3)
	restartCancel()
	d2.stopUserProjectionWorkers()
}

func TestUserProjectionConsumerGapRecoversOnlyAffectedProjectFromVerifiedSnapshot(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	projectID, err := appconfig.ProjectIDForRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	project := appconfig.Project{ID: projectID, Name: "P", Path: root}
	if err := appconfig.SaveProjectsRegistry(&appconfig.ProjectsRegistry{Projects: []appconfig.Project{project}}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := issues.NewClientAtPath(filepath.Join(root, ".azedarach", "azedarach.db"), logger)
	if err := client.OpenProjectionDeltaStore(); err != nil {
		t.Fatal(err)
	}
	defer client.CloseDB()
	issueID, err := client.Create(context.Background(), issues.CreateTaskParams{Title: "source", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	store, err := userstore.Open(filepath.Join(home, ".azedarach", "azedarach.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bad := userstore.ProjectDeltaState{ProjectID: projectID, Cursor: 9, Hash: "impossible", Initialized: true, Projector: issueProjectionProjector()}
	if err := store.ReplaceProject(context.Background(), userstore.ProjectInput{ProjectID: projectID, Name: "P", Path: root, DBPath: filepath.Join(root, ".azedarach", "azedarach.db"), Delta: &bad}); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		cfg:                     Config{RepoDir: root, Logger: logger},
		issues:                  client,
		issueClientsByProject:   map[string]*issues.Client{projectID: client},
		issueClientsByRoot:      map[string]*issues.Client{daemonStoreRootKey(root): client},
		userStore:               store,
		userProjectionConsumers: map[string]*userProjectionConsumerHandle{},
	}
	var recoveries atomic.Int32
	d.userStoreProjectLockHook = func(_ string, entering bool) {
		if entering {
			recoveries.Add(1)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.ensureUserProjectionConsumers(ctx, []appconfig.Project{project})
	waitForGlobalProjectedTask(t, store, projectID, issueID, domain.StatusOpen, 1)
	state, err := store.ProjectDeltaState(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Cursor != 1 || state.Hash == "impossible" || recoveries.Load() != 1 {
		t.Fatalf("gap recovery state=%+v recoveries=%d", state, recoveries.Load())
	}
	cancel()
	d.stopUserProjectionWorkers()
}

func TestIssueDeltaCoverageKeepsRuntimeAndOwnershipMutationsOnLegacyRepair(t *testing.T) {
	for _, command := range []string{"task.ownership.claim", "task.update_status", "task.close", "session.start", "interaction.answer", "git.merge"} {
		if commandCoveredByIssueProjectionDelta(command) {
			t.Fatalf("runtime/ownership command %q bypasses transitional repair", command)
		}
	}
	for _, command := range []string{"task.create", "task.update_details", "task.append_notes", "task.dependency.add", commandSyncRun} {
		if !commandCoveredByIssueProjectionDelta(command) {
			t.Fatalf("complete-value issue command %q did not use delta materialization", command)
		}
	}
}

func TestMergeRootProjectionSourcesRetainsLastKnownTerminalHash(t *testing.T) {
	current := []protocol.ProjectionSourceRange{{Authority: "project", SourceFrom: "1", SourceTo: "2", TerminalHash: "known", Transitional: true}}
	next := []protocol.ProjectionSourceRange{{Authority: "project", SourceFrom: "3", SourceTo: "3", Transitional: true}}
	merged := mergeRootProjectionSources(current, next)
	if len(merged) != 1 || merged[0].SourceFrom != "1" || merged[0].SourceTo != "3" || merged[0].TerminalHash != "known" {
		t.Fatalf("merged sources = %+v", merged)
	}
}

func TestEnsureUserProjectionConsumersCancelsRemovedProject(t *testing.T) {
	consumerCtx, consumerCancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	drained := make(chan struct{})
	go func() {
		<-consumerCtx.Done()
		close(drained)
		close(done)
	}()
	d := &Daemon{
		userStore: &userstore.Store{},
		userProjectionConsumers: map[string]*userProjectionConsumerHandle{
			"removed": {path: "/removed", cancel: consumerCancel, done: done},
		},
	}
	d.ensureUserProjectionConsumers(context.Background(), nil)
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("removed project consumer was not canceled")
	}
	d.userProjectionConsumerMu.Lock()
	defer d.userProjectionConsumerMu.Unlock()
	if len(d.userProjectionConsumers) != 0 {
		t.Fatalf("removed project consumer survived: %+v", d.userProjectionConsumers)
	}
}

func waitForGlobalProjectedTask(t *testing.T, store *userstore.Store, projectID, issueID string, status domain.Status, cursor uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := store.Snapshot(context.Background(), "")
		if err == nil {
			for _, project := range snapshot.Projects {
				if project.ProjectID != projectID || project.DeltaCursor < cursor {
					continue
				}
				for _, task := range project.Tasks {
					if task.ID.String() == issueID && task.Status == status && project.Freshness == protocol.GlobalProjectionFreshnessFresh {
						return
					}
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("project %s issue %s did not reach status=%s cursor=%d", projectID, issueID, status, cursor)
}
