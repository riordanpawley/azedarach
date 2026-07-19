package daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/daemon/userstore"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/sqliteutil"
)

func TestGlobalProjectionTerminalSessionSuppressionConvergesBootstrapAndIncremental(t *testing.T) {
	ctx := context.Background()
	home, rootA, rootB := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	projectForRoot := func(root, name string) appconfig.Project {
		t.Helper()
		projectID, err := appconfig.ProjectIDForRoot(root)
		if err != nil {
			t.Fatal(err)
		}
		return appconfig.Project{ID: projectID, Name: name, Path: root}
	}
	projectA, projectB := projectForRoot(rootA, "A"), projectForRoot(rootB, "B")
	projectAID := appconfig.RegisteredProjectID(projectA)
	projectBID := appconfig.RegisteredProjectID(projectB)

	openProject := func(root string) (*issues.Client, *daemonstate.RuntimeStateStore) {
		t.Helper()
		dbPath := filepath.Join(root, ".azedarach", "azedarach.db")
		client := issues.NewClientAtPath(dbPath, logger)
		if err := client.OpenProjectionDeltaStore(); err != nil {
			t.Fatal(err)
		}
		runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(dbPath, logger)
		t.Cleanup(func() {
			_ = runtimeStore.Close()
			_ = client.CloseDB()
		})
		return client, runtimeStore
	}
	clientA, runtimeA := openProject(rootA)
	clientB, runtimeB := openProject(rootB)
	closedID, err := clientA.Create(ctx, issues.CreateTaskParams{Title: "closed", Type: domain.TypeBug, Status: domain.StatusDone})
	if err != nil {
		t.Fatal(err)
	}
	closingID, err := clientA.Create(ctx, issues.CreateTaskParams{Title: "stop then close", Type: domain.TypeBug, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	liveID, err := clientB.Create(ctx, issues.CreateTaskParams{Title: "live", Type: domain.TypeTask, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.July, 17, 0, 18, 45, 0, time.UTC)
	closedSessionID := naming.CanonicalSessionID(projectAID, closedID)
	for _, session := range []daemonstate.Session{
		{ID: closedSessionID, IssueID: closedID, Role: daemonstate.SessionRoleWorker, ScopeKind: daemonstate.SessionScopeIssue, ScopeID: closedID, State: daemonstate.SessionStateStopped, ObservedState: daemonstate.SessionStateStopped, UpdatedAt: now},
		{ID: closedSessionID + ".pane-12", IssueID: closedID, Role: daemonstate.SessionRoleWorker, ScopeKind: daemonstate.SessionScopeIssue, ScopeID: closedID, State: daemonstate.SessionStatePaused, ObservedState: daemonstate.SessionStatePaused, Activity: "idle", ActivitySource: "hooks", UpdatedAt: now.Add(-time.Second)},
		{ID: naming.CanonicalSessionID(projectAID, closingID), IssueID: closingID, Role: daemonstate.SessionRoleWorker, ScopeKind: daemonstate.SessionScopeIssue, ScopeID: closingID, State: daemonstate.SessionStateRunning, ObservedState: daemonstate.SessionStateRunning, Activity: "busy", ActivitySource: "hooks", UpdatedAt: now},
	} {
		if err := upsertSessionStateFixture(runtimeA, ctx, projectAID, session); err != nil {
			t.Fatal(err)
		}
	}
	liveSessionID := naming.CanonicalSessionID(projectBID, liveID)
	if err := upsertSessionStateFixture(runtimeB, ctx, projectBID, daemonstate.Session{ID: liveSessionID, IssueID: liveID, Role: daemonstate.SessionRoleWorker, ScopeKind: daemonstate.SessionScopeIssue, ScopeID: liveID, State: daemonstate.SessionStateRunning, ObservedState: daemonstate.SessionStateRunning, Activity: "busy", ActivitySource: "hooks", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	userStore, err := userstore.Open(filepath.Join(home, ".azedarach", "azedarach.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = userStore.Close() })
	d := &Daemon{
		cfg: Config{RepoDir: rootA, Logger: logger}, userStore: userStore, sessionStore: daemonstate.NewStore(),
		issues:                 clientA,
		issueClientsByProject:  map[string]*issues.Client{projectAID: clientA, projectBID: clientB},
		issueClientsByRoot:     map[string]*issues.Client{daemonStoreRootKey(rootA): clientA, daemonStoreRootKey(rootB): clientB},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectAID: runtimeA, projectBID: runtimeB},
	}
	// Even an exact live snapshot cannot turn a terminal/stopped divergence into
	// a healthy global session.
	if _, err := d.sessionStore.UpsertSession(projectAID, closedSessionID, closedID, daemonstate.SessionStateRunning); err != nil {
		t.Fatal(err)
	}

	assertProjection := func(closedHasSession, closingHasSession, liveHasSession bool) {
		t.Helper()
		snapshot, err := userStore.Snapshot(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		byProject := make(map[string]map[string]domain.Task, len(snapshot.Projects))
		for _, project := range snapshot.Projects {
			byProject[project.ProjectID] = make(map[string]domain.Task, len(project.Tasks))
			for _, task := range project.Tasks {
				byProject[project.ProjectID][task.ID.String()] = task
			}
		}
		closed, found := byProject[projectAID][closedID]
		if !found {
			t.Fatalf("closed issue %s/%s missing from global projection", projectAID, closedID)
		}
		if got := closed.Session != nil || closed.HasTmuxSession; got != closedHasSession {
			t.Fatalf("closed projection session=%+v has_tmux_session=%t, want presence=%t", closed.Session, closed.HasTmuxSession, closedHasSession)
		}
		closing, found := byProject[projectAID][closingID]
		if !found {
			t.Fatalf("stop-close issue %s/%s missing from global projection", projectAID, closingID)
		}
		if got := closing.Session != nil || closing.HasTmuxSession; got != closingHasSession {
			t.Fatalf("stop-close projection session=%+v has_tmux_session=%t, want presence=%t", closing.Session, closing.HasTmuxSession, closingHasSession)
		}
		live, found := byProject[projectBID][liveID]
		if !found {
			t.Fatalf("live issue %s/%s missing from global projection", projectBID, liveID)
		}
		if got := live.Session != nil && live.HasTmuxSession; got != liveHasSession {
			t.Fatalf("cross-project live projection session=%+v has_tmux_session=%t, want presence=%t", live.Session, live.HasTmuxSession, liveHasSession)
		}
	}

	for _, project := range []appconfig.Project{projectA, projectB} {
		if err := d.refreshRegisteredUserProject(ctx, project); err != nil {
			t.Fatal(err)
		}
	}
	assertProjection(false, true, true)
	// Full bootstrap/recovery is idempotent and remains project-scoped.
	if err := d.refreshRegisteredUserProject(ctx, projectA); err != nil {
		t.Fatal(err)
	}
	assertProjection(false, true, true)

	// Supported materialized current-state writes now reject the production
	// corruption at the root-user projection boundary.
	staleStartedAt := now.Add(-time.Hour)
	stale := domain.Task{ID: naming.IssueID(closedID), Status: domain.StatusDone, Type: domain.TypeBug, UpdatedAt: now, HasTmuxSession: true, Session: &domain.Session{IssueID: naming.IssueID(closedID), State: domain.SessionPaused, Activity: "idle", ActivitySource: "hooks", StartedAt: &staleStartedAt, UpdatedAt: now.Add(-time.Second)}}
	if err := userStore.ApplyProjectMaterializedIssues(ctx, projectAID, []userstore.ProjectDeltaChange{{IssueID: closedID, Issue: &stale}}); err != nil {
		t.Fatal(err)
	}
	assertProjection(false, true, true)

	applyNextObservation := func(issueID string) {
		t.Helper()
		if _, err := clientA.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventProgressRecorded, Source: "test"}); err != nil {
			t.Fatal(err)
		}
		state, err := userStore.ProjectDeltaState(ctx, projectAID)
		if err != nil {
			t.Fatal(err)
		}
		batch, err := NewProjectionDeltaAuthority(clientA).List(ctx, protocol.DefaultProjectID, state.Cursor, rootProjectionDeltaBatchLimit)
		if err != nil {
			t.Fatal(err)
		}
		remapProjectionDeltaBatch(&batch, projectAID)
		if err := protocol.VerifyProjectionDeltaBatch(batch, state.Cursor, issueProjectionProjector()); err != nil {
			t.Fatal(err)
		}
		changes, err := decodeUserProjectionChanges(batch)
		if err != nil {
			t.Fatal(err)
		}
		changes, err = d.hydrateUserProjectionChanges(ctx, projectAID, clientA, batch, changes)
		if err != nil {
			t.Fatal(err)
		}
		next := userstore.ProjectDeltaState{ProjectID: projectAID, Cursor: batch.DeliveryToCursor, SourceVector: mergeRootProjectionSources(state.SourceVector, batch.SourceVector), Projector: batch.Projector, Initialized: true}
		next.Hash = chainRootProjectDelta(state, next, batch.SemanticChecksum)
		if err := userStore.ApplyProjectDelta(ctx, userstore.ProjectDeltaApply{Project: userstore.CatalogProject{ProjectID: projectAID, Name: projectA.Name, Path: rootA, DBPath: filepath.Join(rootA, ".azedarach", "azedarach.db")}, Expected: state, Next: next, Changes: changes}); err != nil {
			t.Fatal(err)
		}
	}
	applyNextObservation(closedID)
	assertProjection(false, true, true)
	applyNextObservation(closedID)
	assertProjection(false, true, true)

	// The active path must converge immediately when stop intent is persisted
	// before lifecycle close, without needing a full project rebuild.
	if err := upsertSessionStateFixture(runtimeA, ctx, projectAID, daemonstate.Session{
		ID: naming.CanonicalSessionID(projectAID, closingID), IssueID: closingID,
		Role: daemonstate.SessionRoleWorker, ScopeKind: daemonstate.SessionScopeIssue, ScopeID: closingID,
		State: daemonstate.SessionStateStopped, ObservedState: daemonstate.SessionStateStopped, UpdatedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if err := clientA.Update(ctx, closingID, domain.StatusDone); err != nil {
		t.Fatal(err)
	}
	applyNextObservation(closingID)
	assertProjection(false, false, true)
}

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
	if err := client.Archive(context.Background(), issueID); err != nil {
		t.Fatal(err)
	}
	waitForGlobalProjectionCursor(t, store, projectID, 4)
	archivedSnapshot, err := store.Snapshot(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(archivedSnapshot.Projects) != 1 || len(archivedSnapshot.Projects[0].Tasks) != 1 || !archivedSnapshot.Projects[0].Tasks[0].State.IsArchived() {
		t.Fatalf("archived keyed delta did not replace the live value: %+v", archivedSnapshot)
	}
	archivedView, err := projectGlobalView(domain.DefaultBoardView(), archivedSnapshot.Projects)
	if err != nil {
		t.Fatal(err)
	}
	if len(archivedView.Items) != 0 {
		t.Fatalf("archived keyed delta left a live global card: %+v", archivedView)
	}
	laterID, err := client.Create(context.Background(), issues.CreateTaskParams{Title: "later", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	waitForGlobalProjectedTask(t, store, projectID, laterID, domain.StatusOpen, 5)
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
	if component.Cursor != 5 || component.Hash == "" || len(component.SourceVector) == 0 || component.Projector != issueProjectionProjector() {
		t.Fatalf("incremental vector health = %+v", component)
	}
	restartCancel()
	d2.stopUserProjectionWorkers()
}

func waitForGlobalProjectionCursor(t *testing.T, store *userstore.Store, projectID string, cursor uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state, err := store.ProjectDeltaState(context.Background(), projectID)
		if err == nil && state.Cursor >= cursor {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("project %s did not reach cursor=%d", projectID, cursor)
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

func TestUserProjectionConsumerCleansMissingObservationOnlyIssue(t *testing.T) {
	ctx := context.Background()
	client := issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	if err := client.OpenProjectionDeltaStore(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "deleted", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	event, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventProgressRecorded, Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Delete(ctx, issueID); err != nil {
		t.Fatal(err)
	}
	batch := protocol.ProjectionDeltaBatch{EmptyAdvances: []protocol.ProjectionEmptyAdvance{{Source: protocol.ProjectionSourceRange{Authority: "legacy_issue_observation", SourceTo: fmt.Sprint(event.ID)}}}}
	d := &Daemon{cfg: Config{Logger: slog.Default()}, issues: client, issueClientsByProject: map[string]*issues.Client{"p": client}}
	changes, err := d.hydrateUserProjectionChanges(ctx, "p", client, batch, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].IssueID != issueID || !changes[0].Delete {
		t.Fatalf("missing observation-only issue changes = %+v, want cleanup", changes)
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
	d.recordManagedAgentIdentityProjection(daemonstate.ManagedAgentIdentity{
		ProjectID: "removed", SessionID: "az-1", LogicalPaneID: "agent", TmuxPaneID: "7",
		PanePID: 123, AgentIncarnation: "removed-incarnation", ObservedAt: time.Now().UTC(),
	}, true)
	d.recordManagedAgentIdentityProjection(daemonstate.ManagedAgentIdentity{
		ProjectID: "retained", SessionID: "az-2", LogicalPaneID: "agent", TmuxPaneID: "8",
		PanePID: 456, AgentIncarnation: "retained-incarnation", ObservedAt: time.Now().UTC(),
	}, true)
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
	if _, found := d.projectedManagedAgentIdentity("removed", "az-1", "agent"); found {
		t.Fatal("removed project retained managed-agent identity projection")
	}
	if _, found := d.projectedManagedAgentIdentity("retained", "az-2", "agent"); !found {
		t.Fatal("project-prefix purge removed unrelated project projection")
	}
}

func TestGlobalProjectionRealPathsDrainCanceledContentionAndConverge(t *testing.T) {
	ctx := context.Background()
	home, rootA, rootB := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	projectForRoot := func(root, name string) appconfig.Project {
		t.Helper()
		projectID, err := appconfig.ProjectIDForRoot(root)
		if err != nil {
			t.Fatal(err)
		}
		return appconfig.Project{ID: projectID, Name: name, Path: root}
	}
	projectA, projectB := projectForRoot(rootA, "contention-a"), projectForRoot(rootB, "contention-b")
	projectAID, projectBID := appconfig.RegisteredProjectID(projectA), appconfig.RegisteredProjectID(projectB)

	openProject := func(root string) (*issues.Client, *daemonstate.RuntimeStateStore) {
		t.Helper()
		dbPath := filepath.Join(root, ".azedarach", "azedarach.db")
		client := issues.NewClientAtPath(dbPath, logger)
		if err := client.OpenProjectionDeltaStore(); err != nil {
			t.Fatal(err)
		}
		runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(dbPath, logger)
		t.Cleanup(func() {
			_ = runtimeStore.Close()
			_ = client.CloseDB()
		})
		return client, runtimeStore
	}
	clientA, runtimeA := openProject(rootA)
	clientB, runtimeB := openProject(rootB)
	issueA, err := clientA.Create(ctx, issues.CreateTaskParams{Title: "session observation", Type: domain.TypeTask, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	issueB, err := clientB.Create(ctx, issues.CreateTaskParams{Title: "worktree observation", Type: domain.TypeTask, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}

	userDBPath := filepath.Join(home, ".azedarach", "azedarach.db")
	rootStore, err := userstore.Open(userDBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rootStore.Close() })
	d := &Daemon{
		cfg:                    Config{RepoDir: rootA, Logger: logger},
		hub:                    publish.NewHub(16, 8, logger),
		userStore:              rootStore,
		sessionStore:           daemonstate.NewStore(),
		issues:                 clientA,
		issueClientsByProject:  map[string]*issues.Client{projectAID: clientA, projectBID: clientB},
		issueClientsByRoot:     map[string]*issues.Client{daemonStoreRootKey(rootA): clientA, daemonStoreRootKey(rootB): clientB},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectAID: runtimeA, projectBID: runtimeB},
		runtimeStoresByRoot:    map[string]*daemonstate.RuntimeStateStore{daemonStoreRootKey(rootA): runtimeA, daemonStoreRootKey(rootB): runtimeB},
		materializers:          map[string]*projectReadMaterializer{},
		materializersStarted:   true,
		materializersContext:   ctx,
	}
	for _, project := range []appconfig.Project{projectA, projectB} {
		if err := d.refreshRegisteredUserProject(ctx, project); err != nil {
			t.Fatal(err)
		}
	}
	for projectID, client := range map[string]*issues.Client{projectAID: clientA, projectBID: clientB} {
		if _, err := d.ensureProjectReadMaterializer(ctx, projectID, client); err != nil {
			t.Fatalf("bootstrap materializer %s: %v", projectID, err)
		}
	}

	sessionID := naming.CanonicalSessionID(projectAID, issueA)
	now := time.Date(2026, time.July, 19, 11, 12, 13, 0, time.UTC)
	if err := runtimeA.UpsertSessionState(ctx, projectAID, daemonstate.Session{
		ID: sessionID, IssueID: issueA, Role: daemonstate.SessionRoleWorker,
		ScopeKind: daemonstate.SessionScopeIssue, ScopeID: issueA,
		State: daemonstate.SessionStateRunning, ObservedState: daemonstate.SessionStateRunning,
		Activity: "idle", ActivitySource: "hooks", UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	holderEntered := make(chan struct{})
	releaseHolder := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-releaseHolder:
		default:
			close(releaseHolder)
		}
	})
	holderDone := make(chan error, 1)
	go func() {
		holderCtx := sqliteutil.ContextWithWriteOperation(ctx, "test.root_projection_holder")
		holderDone <- sqliteutil.WithWriteLockContext(holderCtx, userDBPath, func(context.Context) error {
			close(holderEntered)
			<-releaseHolder
			return nil
		})
	}()
	<-holderEntered

	staleReconcileCtx, cancelStaleReconcile := context.WithCancel(ctx)
	staleMaterializeCtx, cancelStaleMaterialize := context.WithCancel(ctx)
	staleReconcileDone := make(chan error, 1)
	staleMaterializeDone := make(chan error, 1)
	go func() { staleReconcileDone <- d.refreshRegisteredUserProject(staleReconcileCtx, projectA) }()
	go func() {
		staleMaterializeDone <- d.refreshProjectReadRuntimeForIssues(staleMaterializeCtx, projectBID, []string{issueB})
	}()
	for sqliteutil.WriteLockResourceDiagnostics(userDBPath).Waiters != 2 {
		runtime.Gosched()
	}
	cancelStaleReconcile()
	cancelStaleMaterialize()
	for name, result := range map[string]<-chan error{
		"startup reconcile":       staleReconcileDone,
		"runtime materialization": staleMaterializeDone,
	} {
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled %s error = %v, want context.Canceled", name, err)
		}
	}
	if diagnostic := sqliteutil.WriteLockResourceDiagnostics(userDBPath); diagnostic.Waiters != 0 || diagnostic.Holder != "test.root_projection_holder" {
		t.Fatalf("canceled real-path waiters remained: %+v", diagnostic)
	}

	// Production daemon construction installs this writer before concurrent
	// observation loops start; mirror that lifecycle in the hand-built fixture.
	runtimeWriter := d.runtimeProjectionStateWriter()
	observationDone := make(chan error, 2)
	go func() {
		observationDone <- d.persistObservedRuntimeProjection(ctx, projectAID, protocol.Metadata{ProjectID: naming.ProjectID(projectAID)}, daemonstate.Session{
			ID: sessionID, IssueID: issueA, ObservedState: daemonstate.SessionStateRunning,
			Activity: "busy", ActivitySource: "hooks", UpdatedAt: now.Add(time.Second),
		})
	}()
	worktreePath := filepath.Join(rootB, ".azedarach", "worktrees", issueB)
	go func() {
		observationDone <- runtimeWriter.PersistWorktreeProjection(ctx, projectBID, issueB, worktreePath, "review/"+issueB)
	}()
	for sqliteutil.WriteLockResourceDiagnostics(userDBPath).Waiters != 2 {
		runtime.Gosched()
	}
	close(releaseHolder)
	if err := <-holderDone; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-observationDone; err != nil {
			t.Fatalf("runtime observation: %v", err)
		}
	}
	if diagnostic := sqliteutil.WriteLockResourceDiagnostics(userDBPath); diagnostic.Waiters != 0 || diagnostic.Holder != "" {
		t.Fatalf("released root writer retained waiters: %+v", diagnostic)
	}

	if session, found, err := runtimeA.GetSessionState(ctx, projectAID, sessionID); err != nil || !found || session.Activity != "busy" {
		t.Fatalf("session observation = %+v found=%t err=%v", session, found, err)
	}
	if worktree, found, err := runtimeB.GetWorktreeStateByIssueID(ctx, projectBID, issueB); err != nil || !found || worktree.Path != worktreePath {
		t.Fatalf("worktree observation = %+v found=%t err=%v", worktree, found, err)
	}

	// Fresh startup work and the next recurring materialization cycle must both
	// succeed after the canceled backlog, for both projects.
	for _, project := range []appconfig.Project{projectA, projectB} {
		if err := d.refreshRegisteredUserProject(ctx, project); err != nil {
			t.Fatalf("fresh startup reconcile %s: %v", project.Name, err)
		}
	}
	for projectID, issueID := range map[string]string{projectAID: issueA, projectBID: issueB} {
		if err := d.refreshProjectReadRuntimeForIssues(ctx, projectID, []string{issueID}); err != nil {
			t.Fatalf("next recurring materialization %s: %v", projectID, err)
		}
	}
	snapshot, err := rootStore.Snapshot(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	seenSession, seenWorktree := false, false
	for _, project := range snapshot.Projects {
		for _, task := range project.Tasks {
			seenSession = seenSession || project.ProjectID == projectAID && task.ID.String() == issueA && task.Session != nil && task.Session.Activity == "busy"
			seenWorktree = seenWorktree || project.ProjectID == projectBID && task.ID.String() == issueB && task.HasWorktree
		}
	}
	if !seenSession || !seenWorktree {
		t.Fatalf("root projection did not converge observations: session=%t worktree=%t", seenSession, seenWorktree)
	}

	missingRoot := t.TempDir()
	missingProject := projectForRoot(missingRoot, "missing-source")
	missingID := appconfig.RegisteredProjectID(missingProject)
	missingDBPath := filepath.Join(missingRoot, ".azedarach", "azedarach.db")
	missingClient := issues.NewClientAtPath(missingDBPath, logger)
	if err := missingClient.OpenProjectionDeltaStore(); err != nil {
		t.Fatal(err)
	}
	if err := missingClient.CloseDB(); err != nil {
		t.Fatal(err)
	}
	d.issueClientsByProject[missingID] = missingClient
	d.issueClientsByRoot[daemonStoreRootKey(missingRoot)] = missingClient
	if err := d.refreshRegisteredUserProject(ctx, missingProject); err == nil || !strings.Contains(err.Error(), "projection delta store is not open") {
		t.Fatalf("closed source error = %v, want visible projection-store failure", err)
	}
	snapshot, err = rootStore.Snapshot(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	failureVisible := false
	for _, project := range snapshot.Projects {
		failureVisible = failureVisible || project.ProjectID == missingID && project.Freshness == protocol.GlobalProjectionFreshnessUnavailable && strings.Contains(project.LastError, "projection delta store is not open")
	}
	if !failureVisible {
		t.Fatalf("genuine source failure absent from root projection: %+v", snapshot.Projects)
	}

	diagnostics := d.sqliteStoreDiagnostics()
	projectADBPath := filepath.Join(rootA, ".azedarach", "azedarach.db")
	projectBDBPath := filepath.Join(rootB, ".azedarach", "azedarach.db")
	expectedAuthorities := map[string][]string{
		projectADBPath + "\x00issues":                   {projectAID},
		projectADBPath + "\x00runtime":                  {projectAID},
		projectADBPath + "\x00runtime-managed-identity": {projectAID},
		projectBDBPath + "\x00issues":                   {projectBID},
		projectBDBPath + "\x00runtime":                  {projectBID},
		projectBDBPath + "\x00runtime-managed-identity": {projectBID},
		missingDBPath + "\x00issues":                    {missingID},
		userDBPath + "\x00user_projection":              nil,
	}
	if len(diagnostics) != len(expectedAuthorities) {
		t.Fatalf("SQLite authority count = %d, want %d: %+v", len(diagnostics), len(expectedAuthorities), diagnostics)
	}
	for _, diagnostic := range diagnostics {
		key := diagnostic.DBPath + "\x00" + diagnostic.Owner
		expectedProjects, found := expectedAuthorities[key]
		if !found {
			t.Fatalf("unexpected SQLite authority %q: %+v", key, diagnostic)
		}
		if strings.Join(diagnostic.ProjectIDs, "\x00") != strings.Join(expectedProjects, "\x00") {
			t.Fatalf("SQLite authority %q projects = %v, want %v", key, diagnostic.ProjectIDs, expectedProjects)
		}
		delete(expectedAuthorities, key)
		if diagnostic.InUse != 0 || (diagnostic.Open && (diagnostic.MaxOpenConnections <= 0 || diagnostic.OpenConnections > diagnostic.MaxOpenConnections)) || (!diagnostic.Open && diagnostic.OpenConnections != 0) {
			t.Fatalf("unbounded or retained SQLite handles: %+v", diagnostic)
		}
		if diagnostic.SQLiteWriteWaiters != 0 || diagnostic.SQLiteWriteHolder != "" {
			t.Fatalf("retained SQLite writer state: %+v", diagnostic)
		}
	}

	// This connection bypasses the process-local writer lock, matching a second
	// daemon/process contending through SQLite itself.
	externalDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(userDBPath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = externalDB.Close() })
	externalConn, err := externalDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer externalConn.Close()
	if _, err := externalConn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	externalRefreshCtx, cancelExternalRefresh := context.WithCancel(ctx)
	externalRefreshDone := make(chan error, 1)
	go func() { externalRefreshDone <- d.refreshRegisteredUserProject(externalRefreshCtx, projectA) }()
	for {
		diagnostic := sqliteutil.WriteLockResourceDiagnostics(userDBPath)
		if diagnostic.Holder == "user_projection.begin_project_refresh" {
			break
		}
		select {
		case err := <-externalRefreshDone:
			t.Fatalf("external SQLite contender returned before cancellation: %v", err)
		default:
			runtime.Gosched()
		}
	}
	cancelExternalRefresh()
	if err := <-externalRefreshDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("external SQLite contention cancellation = %v, want context.Canceled", err)
	}
	if _, err := externalConn.ExecContext(ctx, `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	if diagnostic := sqliteutil.WriteLockResourceDiagnostics(userDBPath); diagnostic.Waiters != 0 || diagnostic.Holder != "" {
		t.Fatalf("external contention retained process writer state: %+v", diagnostic)
	}
	if err := d.refreshRegisteredUserProject(ctx, projectA); err != nil {
		t.Fatalf("fresh reconcile after external SQLite writer: %v", err)
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
