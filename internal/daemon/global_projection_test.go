package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/userstore"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

func TestUserProjectionMutationRefreshIsRateBoundedAndNonOverlapping(t *testing.T) {
	const interval = 40 * time.Millisecond
	var mu sync.Mutex
	var starts []time.Time
	inFlight := 0
	maxInFlight := 0
	started := make(chan struct{}, 3)
	d := &Daemon{
		userStore:                &userstore.Store{},
		userStoreRefreshPending:  map[string]bool{},
		userStoreRefreshDirty:    map[string]bool{},
		userStoreRefreshInterval: interval,
		userStoreRefreshProjectFn: func(_ context.Context, projectID string) error {
			if projectID != "busy" {
				t.Fatalf("projectID = %q, want busy", projectID)
			}
			mu.Lock()
			starts = append(starts, time.Now())
			inFlight++
			if inFlight > maxInFlight {
				maxInFlight = inFlight
			}
			mu.Unlock()
			started <- struct{}{}
			time.Sleep(8 * time.Millisecond)
			mu.Lock()
			inFlight--
			mu.Unlock()
			return nil
		},
	}

	d.enqueueUserProjectionRefresh("busy")
	for pass := 0; pass < 3; pass++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatalf("refresh pass %d did not start", pass+1)
		}
		if pass < 2 {
			// Enqueue while this pass is active so one dirty replay is required.
			d.enqueueUserProjectionRefresh("busy")
		}
	}
	d.userStoreRefreshWG.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(starts) != 3 {
		t.Fatalf("refresh starts = %d at %v, want exactly 3 event-driven passes", len(starts), starts)
	}
	if maxInFlight != 1 {
		t.Fatalf("max in-flight refreshes = %d, want 1", maxInFlight)
	}
	for i := 1; i < len(starts); i++ {
		if gap := starts[i].Sub(starts[i-1]); gap < interval-5*time.Millisecond {
			t.Fatalf("refresh gap %d = %s, want at least %s", i, gap, interval-5*time.Millisecond)
		}
	}
}

func TestUserProjectionMutationRefreshSchedulesProjectsIndependently(t *testing.T) {
	aStarted := make(chan struct{})
	bStarted := make(chan struct{})
	releaseA := make(chan struct{})
	d := &Daemon{
		userStore:                &userstore.Store{},
		userStoreRefreshPending:  map[string]bool{},
		userStoreRefreshDirty:    map[string]bool{},
		userStoreRefreshInterval: 20 * time.Millisecond,
		userStoreRefreshProjectFn: func(_ context.Context, projectID string) error {
			switch projectID {
			case "a":
				close(aStarted)
				<-releaseA
			case "b":
				close(bStarted)
			}
			return nil
		},
	}

	d.enqueueUserProjectionRefresh("a")
	<-aStarted
	d.enqueueUserProjectionRefresh("b")
	select {
	case <-bStarted:
	case <-time.After(time.Second):
		close(releaseA)
		t.Fatal("project b refresh starved behind project a")
	}
	close(releaseA)
	d.userStoreRefreshWG.Wait()
}

func TestStopUserProjectionWorkersCancelsThrottledDirtyReplay(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	d := &Daemon{
		userStore:                &userstore.Store{},
		userStoreRefreshPending:  map[string]bool{},
		userStoreRefreshDirty:    map[string]bool{},
		userStoreRefreshInterval: time.Hour,
		userStoreRefreshProjectFn: func(_ context.Context, _ string) error {
			close(firstStarted)
			<-releaseFirst
			return nil
		},
	}

	d.enqueueUserProjectionRefresh("stopping")
	<-firstStarted
	d.enqueueUserProjectionRefresh("stopping")
	close(releaseFirst)
	time.Sleep(10 * time.Millisecond)
	stopped := make(chan struct{})
	go func() {
		d.stopUserProjectionWorkers()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("projection shutdown did not cancel throttled dirty replay")
	}
	d.userStoreRefreshMu.Lock()
	defer d.userStoreRefreshMu.Unlock()
	if len(d.userStoreRefreshPending) != 0 || len(d.userStoreRefreshDirty) != 0 || len(d.userStoreRefreshActive) != 0 {
		t.Fatalf("projection worker state survived shutdown: pending=%v dirty=%v active=%v", d.userStoreRefreshPending, d.userStoreRefreshDirty, d.userStoreRefreshActive)
	}
}

func TestScheduledUserProjectionDirtyCooldownReportsStaleHealth(t *testing.T) {
	store, err := userstore.Open(t.TempDir() + "/user.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err = store.ReplaceProject(context.Background(), userstore.ProjectInput{ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db"}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	d := &Daemon{
		userStore:                store,
		userStoreRefreshPending:  map[string]bool{},
		userStoreRefreshDirty:    map[string]bool{},
		userStoreRefreshActive:   map[string]bool{},
		userStoreRefreshInterval: time.Hour,
		userStoreRefreshProjectFn: func(_ context.Context, _ string) error {
			close(started)
			<-release
			return nil
		},
	}
	d.enqueueUserProjectionRefresh("p")
	resp, err := d.handleGlobalSnapshot(context.Background(), protocol.RequestEnvelope{})
	if err != nil || !resp.OK {
		t.Fatalf("initial queued global snapshot response = %+v, err=%v", resp, err)
	}
	var snapshot protocol.GlobalSnapshotResponseBody
	if err = json.Unmarshal(resp.Body, &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.Partial || len(snapshot.Projects) != 1 || snapshot.Projects[0].Freshness != protocol.GlobalProjectionFreshnessStale {
		t.Fatalf("initial queued snapshot = %+v, want partial stale project", snapshot)
	}
	<-started
	close(release)
	waitForUserProjectionSchedulerState(t, d, "p", true, false)
	d.enqueueUserProjectionRefresh("p")

	resp, err = d.handleGlobalSnapshot(context.Background(), protocol.RequestEnvelope{})
	if err != nil || !resp.OK {
		t.Fatalf("global snapshot response = %+v, err=%v", resp, err)
	}
	snapshot = protocol.GlobalSnapshotResponseBody{}
	if err = json.Unmarshal(resp.Body, &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.Partial || len(snapshot.Projects) != 1 || snapshot.Projects[0].Freshness != protocol.GlobalProjectionFreshnessStale {
		t.Fatalf("dirty cooldown snapshot = %+v, want partial stale project", snapshot)
	}
	var reconcile protocol.RuntimeReconcileResponseBody
	d.readCrossProjectProjectionHealth(context.Background(), "p", &reconcile)
	if reconcile.CrossProjectProjection == nil || reconcile.CrossProjectProjection.Freshness != protocol.GlobalProjectionFreshnessStale {
		t.Fatalf("dirty cooldown reconcile health = %+v, want stale", reconcile.CrossProjectProjection)
	}
	d.stopUserProjectionWorkers()
}

func TestPeriodicUserProjectionRepairSharesMutationSchedule(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	if err := appconfig.SaveProjectsRegistry(&appconfig.ProjectsRegistry{Projects: []appconfig.Project{{ID: "p", Name: "P", Path: root}}}); err != nil {
		t.Fatal(err)
	}
	store, err := userstore.Open(t.TempDir() + "/user.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	const interval = 40 * time.Millisecond
	var mu sync.Mutex
	var starts []time.Time
	inFlight, maxInFlight := 0, 0
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	d := &Daemon{
		userStore:                store,
		userStoreRefreshPending:  map[string]bool{},
		userStoreRefreshDirty:    map[string]bool{},
		userStoreRefreshActive:   map[string]bool{},
		userStoreRefreshInterval: interval,
		userStoreRefreshProjectFn: func(_ context.Context, _ string) error {
			mu.Lock()
			starts = append(starts, time.Now())
			inFlight++
			maxInFlight = max(maxInFlight, inFlight)
			first := len(starts) == 1
			mu.Unlock()
			if first {
				close(firstStarted)
				<-releaseFirst
			}
			mu.Lock()
			inFlight--
			mu.Unlock()
			return nil
		},
	}
	d.enqueueUserProjectionRefresh("p")
	<-firstStarted
	if err = d.scheduleUserProjectionRepair(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(releaseFirst)
	d.userStoreRefreshWG.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(starts) != 2 || maxInFlight != 1 {
		t.Fatalf("repair/mutation starts=%v max_in_flight=%d, want two serialized passes", starts, maxInFlight)
	}
	if gap := starts[1].Sub(starts[0]); gap < interval {
		t.Fatalf("repair replay gap=%s, want at least %s", gap, interval)
	}
}

func TestUserProjectionRealRefreshPathsShareProjectLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	project := appconfig.Project{ID: projectID, Name: "P", Path: root}
	if err := appconfig.SaveProjectsRegistry(&appconfig.ProjectsRegistry{Projects: []appconfig.Project{project}}); err != nil {
		t.Fatal(err)
	}
	issueClient := issues.NewClientAtPath(filepath.Join(root, ".azedarach", "azedarach.db"), slog.Default())
	if _, err = issueClient.ExportProjection(context.Background(), projectID); err != nil {
		t.Fatal(err)
	}
	store, err := userstore.Open(filepath.Join(t.TempDir(), "user.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var mu sync.Mutex
	entries, inFlight, maxInFlight := 0, 0, 0
	firstEntered := make(chan struct{})
	secondEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	d := &Daemon{
		cfg:                      Config{RepoDir: root, Logger: slog.Default()},
		userStore:                store,
		issueClientsByProject:    map[string]*issues.Client{projectID: issueClient},
		userStoreRefreshPending:  map[string]bool{},
		userStoreRefreshDirty:    map[string]bool{},
		userStoreRefreshActive:   map[string]bool{},
		userStoreRefreshInterval: time.Hour,
		userStoreProjectLockHook: func(gotProjectID string, entering bool) {
			if gotProjectID != projectID {
				t.Errorf("critical section project = %q, want %q", gotProjectID, projectID)
			}
			mu.Lock()
			if entering {
				entries++
				inFlight++
				maxInFlight = max(maxInFlight, inFlight)
				entry := entries
				mu.Unlock()
				switch entry {
				case 1:
					close(firstEntered)
					<-releaseFirst
				case 2:
					close(secondEntered)
				}
				return
			}
			inFlight--
			mu.Unlock()
		},
	}

	d.enqueueUserProjectionRefresh(projectID)
	select {
	case <-firstEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduled refresh did not enter the real project critical section")
	}
	explicitDone := make(chan error, 1)
	go func() { explicitDone <- d.refreshUserProjection(context.Background()) }()
	select {
	case <-secondEntered:
		close(releaseFirst)
		t.Fatal("explicit rebuild entered while scheduled refresh held the project lock")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseFirst)
	select {
	case <-secondEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("explicit rebuild did not enter after scheduled refresh released the project lock")
	}
	if err = <-explicitDone; err != nil {
		t.Fatal(err)
	}
	d.stopUserProjectionWorkers()

	mu.Lock()
	defer mu.Unlock()
	if entries != 2 || maxInFlight != 1 || inFlight != 0 {
		t.Fatalf("real refresh critical sections: entries=%d max_in_flight=%d in_flight=%d, want 2/1/0", entries, maxInFlight, inFlight)
	}
}

func waitForUserProjectionSchedulerState(t *testing.T, d *Daemon, projectID string, pending, active bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		d.userStoreRefreshMu.Lock()
		gotPending := d.userStoreRefreshPending[projectID]
		gotActive := d.userStoreRefreshActive[projectID]
		d.userStoreRefreshMu.Unlock()
		if gotPending == pending && gotActive == active {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("scheduler state for %s did not become pending=%v active=%v", projectID, pending, active)
}

func TestGlobalBoardViewCommandsReturnUnavailableWithoutUserStore(t *testing.T) {
	d := &Daemon{}
	tests := []struct {
		name string
		body any
		call func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	}{
		{"get", protocol.BoardViewGetRequestBody{ProjectID: "global"}, d.handleBoardViewGet},
		{"save", protocol.BoardViewSaveRequestBody{ProjectID: "global", View: domain.DefaultBoardView()}, d.handleBoardViewSave},
		{"delete", protocol.BoardViewDeleteRequestBody{ProjectID: "global", ViewID: "custom"}, d.handleBoardViewDelete},
		{"select", protocol.BoardViewSelectRequestBody{ProjectID: "global", ViewID: "default"}, d.handleBoardViewSelect},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(tt.body)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := tt.call(context.Background(), protocol.RequestEnvelope{Body: raw})
			if err != nil {
				t.Fatal(err)
			}
			if resp.OK || resp.Error == nil || resp.Error.Code != protocol.ErrorCodeUnavailable {
				t.Fatalf("response = %+v, want unavailable", resp)
			}
		})
	}
}

func TestGlobalProjectionSharesInvestigationHumanAuthorityAndAcceptance(t *testing.T) {
	ctx := context.Background()
	const projectID = "authority-project"
	projectRoot := t.TempDir()
	issueDBPath := filepath.Join(projectRoot, ".azedarach", "azedarach.db")
	issueClient := issues.NewClientAtPath(issueDBPath, slog.Default())
	t.Cleanup(func() { _ = issueClient.CloseDB() })
	issueID, err := issueClient.Create(ctx, issues.CreateTaskParams{Title: "Human findings", Type: domain.TypeInvestigation, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	globalStore, err := userstore.Open(filepath.Join(t.TempDir(), "user.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = globalStore.Close() })
	d := &Daemon{cfg: Config{RepoDir: projectRoot, Logger: slog.Default()}, issues: issueClient, issueClientsByProject: map[string]*issues.Client{projectID: issueClient}, userStore: globalStore}
	assertPlacement := func(want domain.BoardColumnID) {
		t.Helper()
		if err := d.exportProjectToUserProjection(ctx, projectID, "Authority", projectRoot, issueDBPath, 0); err != nil {
			t.Fatal(err)
		}
		view := domain.OrchestrationBoardView()
		snapshot, err := globalStore.SnapshotForView(ctx, "", &view)
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Projects) != 1 || len(snapshot.Projects[0].Tasks) != 1 {
			t.Fatalf("snapshot = %+v", snapshot.Projects)
		}
		placement, err := view.PlaceTask(snapshot.Projects[0].Tasks[0])
		if err != nil || placement.ColumnID != want {
			t.Fatalf("placement = %+v err=%v want=%s", placement, err, want)
		}
	}
	assertPlacement(domain.BoardColumnWaitingHuman)
	if _, err := issueClient.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventHumanInputProvided, Source: "human", Payload: map[string]any{"investigation_findings_accepted": true}}); err != nil {
		t.Fatal(err)
	}
	assertPlacement(domain.BoardColumnReviewReady)
}

func TestCommandMutatesProjectProjectionIsExplicit(t *testing.T) {
	for _, command := range []string{"task.create", "session.stop", protocol.CommandInteractionAnswer, protocol.CommandRuntimeReconcile} {
		if !commandMutatesProjectProjection(command) {
			t.Errorf("%s should trigger refresh", command)
		}
	}
	for _, command := range []string{"task.list", "task.complete_check", "session.status", protocol.CommandSessionCapture, protocol.CommandGlobalSnapshot, "task.unknown_future_command"} {
		if commandMutatesProjectProjection(command) {
			t.Errorf("%s should not trigger refresh", command)
		}
	}
}

func TestProjectGlobalViewPreservesCollidingScopedIssueIDs(t *testing.T) {
	now := time.Now().UTC()
	projects := []protocol.GlobalProjectSnapshot{{ProjectID: "p-a", Tasks: []domain.Task{{ID: "same", Title: "A", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now}}}, {ProjectID: "p-b", Tasks: []domain.Task{{ID: "same", Title: "B", Status: domain.StatusOpen, Priority: domain.P1, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now}}}}
	projection, err := projectGlobalView(domain.DefaultBoardView(), projects)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Items) != 2 {
		t.Fatalf("items=%d", len(projection.Items))
	}
	if projection.Items[0].Identity.ProjectID == projection.Items[1].Identity.ProjectID {
		t.Fatalf("identities=%+v", projection.Items)
	}
}

func TestProjectGlobalViewLeavesHydratedOutOfViewTaskOutOfOrdering(t *testing.T) {
	now := time.Now().UTC()
	projects := []protocol.GlobalProjectSnapshot{{ProjectID: "p", Tasks: []domain.Task{
		{ID: "excluded-live", Title: "Hydrated title", Status: domain.StatusOpen, Priority: domain.P1, Type: domain.TypeBug, CreatedAt: now, UpdatedAt: now},
		{ID: "active", Title: "Visible", Status: domain.StatusInProgress, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now},
	}}}
	projection, err := projectGlobalView(domain.OrchestrationBoardView(), projects)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Items) != 1 || projection.Items[0].Identity.IssueID != "active" {
		t.Fatalf("configured view ordering included hydrated fallback: %+v", projection.Items)
	}
	if len(projection.KnownTaskIDs) != 2 {
		t.Fatalf("known task IDs = %+v, want both durable tasks", projection.KnownTaskIDs)
	}
}

func TestProjectGlobalTreeViewPreservesBranchesAcrossProjects(t *testing.T) {
	now := time.Now().UTC()
	alphaRoot := naming.IssueID("alpha-root")
	betaRoot := naming.IssueID("beta-root")
	projects := []protocol.GlobalProjectSnapshot{
		{ProjectID: "alpha", Tasks: []domain.Task{
			{ID: alphaRoot, Title: "Ordinary root", Status: domain.StatusInProgress, Priority: domain.P0, UpdatedAt: now},
			{ID: "alpha-child", Title: "Human-waiting child", Status: domain.StatusInProgress, Priority: domain.P4, ParentID: &alphaRoot, Session: &domain.Session{Activity: "waiting-for-human"}, HasTmuxSession: true, UpdatedAt: now},
		}},
		{ProjectID: "beta", Tasks: []domain.Task{
			{ID: betaRoot, Title: "Review root", Status: domain.StatusInReview, Priority: domain.P4, Session: &domain.Session{Activity: string(domain.SessionIdle)}, HasTmuxSession: true, UpdatedAt: now},
			{ID: "beta-child", Title: "Review child", Status: domain.StatusInProgress, Priority: domain.P2, ParentID: &betaRoot, UpdatedAt: now},
		}},
	}

	projection, err := projectGlobalView(domain.TreeBoardView(), projects)
	if err != nil {
		t.Fatal(err)
	}
	want := []protocol.ScopedIssueID{
		{ProjectID: "beta", IssueID: betaRoot},
		{ProjectID: "beta", IssueID: "beta-child"},
		{ProjectID: "alpha", IssueID: alphaRoot},
		{ProjectID: "alpha", IssueID: "alpha-child"},
	}
	if len(projection.Items) != len(want) {
		t.Fatalf("items = %+v, want %d tree items", projection.Items, len(want))
	}
	for i, identity := range want {
		if projection.Items[i].Identity != identity {
			t.Fatalf("item identities = %+v, want %+v", projection.Items, want)
		}
		wantDepth := i % 2
		if projection.Items[i].Depth != wantDepth {
			t.Fatalf("item %s depth = %d, want %d", identity.IssueID, projection.Items[i].Depth, wantDepth)
		}
	}
}

func TestFilterGlobalProjectsUsesCanonicalScopedIdentity(t *testing.T) {
	projects := []protocol.GlobalProjectSnapshot{{ProjectID: "alpha"}, {ProjectID: "beta"}}
	selected := filterGlobalProjects(projects, protocol.GlobalViewScope{Kind: protocol.GlobalViewScopeSelectedProjects, ProjectIDs: []naming.ProjectID{"beta"}})
	if len(selected) != 1 || selected[0].ProjectID != "beta" {
		t.Fatalf("selected projects = %+v", selected)
	}
	current := filterGlobalProjects(projects, protocol.GlobalViewScope{Kind: protocol.GlobalViewScopeCurrentProject, CurrentProjectID: "alpha"})
	if len(current) != 1 || current[0].ProjectID != "alpha" {
		t.Fatalf("current projects = %+v", current)
	}
}

func TestScopedViewKeepsHydratedOutsideScopeTaskKnownButUnprojected(t *testing.T) {
	projects := []protocol.GlobalProjectSnapshot{
		{ProjectID: "alpha", Tasks: []domain.Task{{ID: "same", Status: domain.StatusInProgress}}},
		{ProjectID: "beta", Tasks: []domain.Task{{ID: "same", Status: domain.StatusOpen}}},
	}
	scope := protocol.GlobalViewScope{Kind: protocol.GlobalViewScopeSelectedProjects, ProjectIDs: []naming.ProjectID{"alpha"}}
	projected := filterGlobalProjects(projects, scope)
	if len(projected) != 1 || projected[0].ProjectID != "alpha" {
		t.Fatalf("projection projects = %+v, want alpha only", projected)
	}
	projection, err := projectGlobalView(domain.OrchestrationBoardView(), projected)
	if err != nil {
		t.Fatal(err)
	}
	projection.KnownTaskIDs = augmentGlobalProjectionKnownTasks(projection.KnownTaskIDs, projects)
	if len(projection.Items) != 1 || projection.Items[0].Identity.ProjectID != "alpha" {
		t.Fatalf("scoped items = %+v, want alpha only", projection.Items)
	}
	wantKnown := []protocol.ScopedIssueID{{ProjectID: "alpha", IssueID: "same"}, {ProjectID: "beta", IssueID: "same"}}
	if len(projection.KnownTaskIDs) != len(wantKnown) {
		t.Fatalf("known identities = %+v, want %+v", projection.KnownTaskIDs, wantKnown)
	}
	for i := range wantKnown {
		if projection.KnownTaskIDs[i] != wantKnown[i] {
			t.Fatalf("known identities = %+v, want %+v", projection.KnownTaskIDs, wantKnown)
		}
	}
}
