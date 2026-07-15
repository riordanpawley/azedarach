package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	legacy, err := client.ExportProjection(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	legacy.Tasks = d2.enrichTasksWithSessionState(context.Background(), projectID, legacy.Tasks)
	incremental, err := store.Snapshot(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(incremental.Projects) != 1 || checksumJSON(incremental.Projects[0].Tasks) != checksumJSON(legacy.Tasks) {
		t.Fatalf("shadow materializations diverged: legacy=%s incremental=%s", checksumJSON(legacy.Tasks), checksumJSON(incremental.Projects[0].Tasks))
	}
	legacyView, err := projectGlobalView(domain.DefaultBoardView(), []protocol.GlobalProjectSnapshot{{ProjectID: projectID, Tasks: legacy.Tasks}})
	if err != nil {
		t.Fatal(err)
	}
	incrementalView, err := projectGlobalView(domain.DefaultBoardView(), incremental.Projects)
	if err != nil {
		t.Fatal(err)
	}
	if checksumJSON(legacyView) != checksumJSON(incrementalView) {
		t.Fatalf("shadow user-visible views diverged: legacy=%s incremental=%s", checksumJSON(legacyView), checksumJSON(incrementalView))
	}
	component, err := store.ProjectDeltaState(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if component.Cursor != 3 || component.Hash == "" || len(component.SourceVector) == 0 || component.Projector != issueProjectionProjector() {
		t.Fatalf("incremental vector health = %+v", component)
	}
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

func TestUserProjectionTransientFailuresRetainLastGoodWithoutFullReplacement(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	projectID, err := appconfig.ProjectIDForRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := userstore.Open(filepath.Join(home, ".azedarach", "azedarach.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	task := domain.Task{ID: "last-good", Title: "last good", Type: domain.TypeTask, Status: domain.StatusOpen, UpdatedAt: time.Now().UTC()}
	state := userstore.ProjectDeltaState{ProjectID: projectID, Cursor: 41, Hash: "last-good-vector", Initialized: true, Projector: issueProjectionProjector()}
	if err := store.ReplaceProject(context.Background(), userstore.ProjectInput{ProjectID: projectID, Name: "P", Path: root, DBPath: filepath.Join(root, ".azedarach", "azedarach.db"), Tasks: []domain.Task{task}, Delta: &state}); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, userStore: store}
	var fullReplacements atomic.Int32
	d.userStoreProjectLockHook = func(_ string, entering bool) {
		if entering {
			fullReplacements.Add(1)
		}
	}
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "client unavailable", err: errors.New("issue store unavailable")},
		{name: "transient watch", err: fmt.Errorf("%w: watch", domain.ErrProjectionRetryable)},
		{name: "hydration", err: errors.New("hydrate keyed changes")},
		{name: "apply", err: errors.New("apply project delta")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
			defer cancel()
			if d.retryUnavailableUserProjection(ctx, projectID, tc.err) {
				t.Fatal("retry unexpectedly continued after context deadline")
			}
			snapshot, err := store.Snapshot(context.Background(), "")
			if err != nil {
				t.Fatal(err)
			}
			gotState, err := store.ProjectDeltaState(context.Background(), projectID)
			if err != nil {
				t.Fatal(err)
			}
			if fullReplacements.Load() != 0 || len(snapshot.Projects) != 1 || len(snapshot.Projects[0].Tasks) != 1 || snapshot.Projects[0].Tasks[0].ID != task.ID || gotState.Cursor != state.Cursor || gotState.Hash != state.Hash {
				t.Fatalf("%s replaced last-good projection: replacements=%d snapshot=%+v state=%+v", tc.name, fullReplacements.Load(), snapshot, gotState)
			}
		})
	}
}

func TestUserProjectionConsumerHydratesAffectedIssueForEmptyObservationAdvance(t *testing.T) {
	ctx := context.Background()
	client := issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	if err := client.OpenProjectionDeltaStore(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "findings", Type: domain.TypeInvestigation, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	before, err := NewProjectionDeltaAuthority(client).List(ctx, protocol.DefaultProjectID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventHumanInputProvided, Source: "human", Payload: map[string]any{"investigation_findings_accepted": true}}); err != nil {
		t.Fatal(err)
	}
	batch, err := NewProjectionDeltaAuthority(client).List(ctx, protocol.DefaultProjectID, before.DeliveryToCursor, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Deltas) != 0 || len(batch.EmptyAdvances) != 1 {
		t.Fatalf("acceptance batch = %+v, want one non-semantic source advance", batch)
	}
	d := &Daemon{cfg: Config{Logger: slog.Default()}, issues: client, issueClientsByProject: map[string]*issues.Client{"p": client}}
	changes, err := d.hydrateUserProjectionChanges(ctx, "p", client, batch, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].MaterializedIssue == nil || changes[0].MaterializedIssue.ID.String() != issueID || changes[0].MaterializedIssue.IssueFacts().WaitingHuman {
		t.Fatalf("hydrated empty advance changes = %+v", changes)
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

func TestRoutineProjectionPathsDoNotReintroduceLegacyFullRefreshScheduling(t *testing.T) {
	for _, path := range []string{"daemon.go", "operation_runtime.go", "global_projection.go"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"enqueueUserProjectionRefresh", "enqueueLegacyUserProjectionRefresh", "commandMutatesProjectProjection", "userStoreRefreshDirty", "userStoreRefreshPending"} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatalf("%s reintroduced retired routine projection adapter %q", path, forbidden)
			}
		}
	}
}

func TestProductionInvariantReadersDoNotBypassProjectMaterializer(t *testing.T) {
	for path, forbidden := range map[string][]string{
		"orchestration_lifecycle.go": {"ListParentChildSubtreeWithRuntime", "ListWithRuntime(ctx"},
		"task_bulk_cleanup.go":       {"ListWithRuntime(ctx"},
		"context_risk_commands.go":   {"GetWithDependencyContextRuntime", "ListParentChildSubtreeWithRuntime"},
		"mail_commands.go":           {"ListParentChildSubtreeWithRuntime"},
		"task_commands.go":           {"ListParentChildSubtreeWithRuntime"},
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, symbol := range forbidden {
			if strings.Contains(string(raw), symbol) {
				t.Fatalf("%s bypasses project materializer through %q", path, symbol)
			}
		}
	}
}

func TestEnsureUserProjectionConsumersCancelsRemovedProject(t *testing.T) {
	consumerCtx, consumerCancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	drained := make(chan struct{})
	materializerCtx, materializerCancel := context.WithCancel(context.Background())
	materializerDone := make(chan struct{})
	go func() {
		<-materializerCtx.Done()
		close(materializerDone)
	}()
	go func() {
		<-consumerCtx.Done()
		close(drained)
		close(done)
	}()
	d := &Daemon{
		userStore:     &userstore.Store{},
		materializers: map[string]*projectReadMaterializer{"removed": {projectID: "removed", cancel: materializerCancel, done: materializerDone}},
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
	if d.activeProjectReadMaterializer("removed") != nil {
		t.Fatal("removed project current-state materializer survived")
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
