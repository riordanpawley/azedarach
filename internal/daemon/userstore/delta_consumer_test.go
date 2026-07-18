package userstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func BenchmarkProjectDeltaApplyProductionSized(b *testing.B) {
	store, err := Open(filepath.Join(b.TempDir(), "user.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tasks := make([]domain.Task, 4610)
	for index := range tasks {
		tasks[index] = domain.Task{ID: naming.IssueID(fmt.Sprintf("issue-%04d", index)), Title: "fixture", Status: domain.StatusOpen, Type: domain.TypeTask, CreatedAt: fixed, UpdatedAt: fixed}
	}
	state := testDeltaState("p", 1, "bootstrap")
	if err := store.ReplaceProject(context.Background(), ProjectInput{ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db", Tasks: tasks, Delta: &state}); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		next := testDeltaState("p", state.Cursor+1, fmt.Sprintf("hash-%d", index))
		issue := tasks[index%len(tasks)]
		issue.Title = next.Hash
		if err := store.ApplyProjectDelta(context.Background(), ProjectDeltaApply{Project: CatalogProject{ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db"}, Expected: state, Next: next, Changes: []ProjectDeltaChange{{IssueID: issue.ID.String(), Issue: &issue}}}); err != nil {
			b.Fatal(err)
		}
		state = next
	}
}

func testDeltaState(projectID string, cursor uint64, hash string) ProjectDeltaState {
	return ProjectDeltaState{
		ProjectID: projectID, Cursor: cursor, Hash: hash, Initialized: true,
		SourceVector: []protocol.ProjectionSourceRange{{Authority: "legacy_issue_observation", SourceFrom: "1", SourceTo: "1", Transitional: true}},
		Projector:    protocol.ProjectionProjector{ID: domain.IssueProjectorID, SchemaVersion: domain.IssueProjectionDeltaSchemaVersion, Build: domain.IssueProjectorBuild, Checksum: domain.IssueProjectorChecksum},
	}
}

func TestApplyProjectDeltaIsAtomicIdempotentAndPreservesRuntimeProjection(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	dependency := naming.IssueID("old-parent")
	old := domain.Task{
		ID: "issue", Title: "old", Status: domain.StatusInProgress, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now,
		Dependencies:   []domain.Dependency{{ID: dependency, Type: domain.DependencyParentChild}},
		Session:        &domain.Session{IssueID: "issue", State: domain.SessionBusy, Activity: "waiting_tool", Worktree: "/worktree", UpdatedAt: now},
		HasTmuxSession: true, HasWorktree: true, GitAdditions: 7, GitDeletions: 3, HasUncommittedChanges: true,
		Ownership: &domain.IssueOwnership{OwnerID: "agent", OwnerKind: "agent", ClaimedAt: now},
	}
	initial := testDeltaState("p", 1, "snapshot-one")
	if err := store.ReplaceProject(ctx, ProjectInput{ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db", Tasks: []domain.Task{old}, Delta: &initial}); err != nil {
		t.Fatal(err)
	}
	newParent := naming.IssueID("new-parent")
	nextIssue := domain.CanonicalIssueProjectionTask(old)
	nextIssue.Title = "new"
	nextIssue.Dependencies = []domain.Dependency{{ID: newParent, Type: domain.DependencyParentChild}}
	nextIssue.UpdatedAt = now.Add(time.Second)
	next := testDeltaState("p", 2, "chain-two")
	next.SourceVector[0].SourceTo = "2"
	apply := ProjectDeltaApply{
		Project:  CatalogProject{ProjectID: "p", Name: "P renamed", Path: "/renamed", DBPath: "/renamed/db"},
		Expected: initial, Next: next,
		Changes: []ProjectDeltaChange{{IssueID: "issue", Issue: &nextIssue}},
	}
	if err := store.ApplyProjectDelta(ctx, apply); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyProjectDelta(ctx, apply); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	project := projectSnapshot(t, store, "p")
	if project.Name != "P renamed" || project.DeltaCursor != 2 || project.DeltaHash != "chain-two" || project.Freshness != protocol.GlobalProjectionFreshnessFresh {
		t.Fatalf("project delta component = %#v", project)
	}
	if len(project.Tasks) != 1 {
		t.Fatalf("tasks = %#v", project.Tasks)
	}
	got := project.Tasks[0]
	if got.Title != "new" || got.Session == nil || got.Session.State != domain.SessionBusy || !got.HasTmuxSession || !got.HasWorktree || got.GitAdditions != 7 || got.Ownership == nil || got.Ownership.OwnerID != "agent" {
		t.Fatalf("runtime projection was not preserved: %#v", got)
	}
	if len(got.Dependencies) != 1 || got.Dependencies[0].ID != newParent {
		t.Fatalf("dependencies = %#v", got.Dependencies)
	}
	conflict := apply
	conflict.Next = testDeltaState("p", 3, "wrong")
	if err := store.ApplyProjectDelta(ctx, conflict); !errors.Is(err, ErrProjectDeltaConflict) {
		t.Fatalf("stale apply error = %v", err)
	}
	identityMismatch := apply
	identityMismatch.Next.ProjectID = "other"
	if err := store.ApplyProjectDelta(ctx, identityMismatch); err == nil {
		t.Fatal("cross-project component identity mismatch unexpectedly applied")
	}
}

func TestApplyProjectDeltaConcurrentStoresConvergeOnArchivedValue(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "user.db")
	first, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	ctx := context.Background()
	initial := testDeltaState("p", 1, "one")
	live := domain.Task{ID: "issue", Title: "live", Status: domain.StatusOpen, Type: domain.TypeTask, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := first.ReplaceProject(ctx, ProjectInput{ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db", Tasks: []domain.Task{live}, Delta: &initial}); err != nil {
		t.Fatal(err)
	}
	archived := live
	archived.State, err = domain.NewIssueState(domain.IssueStateParts{Workflow: domain.IssueWorkflowOpen, Archive: domain.IssueArchiveArchived})
	if err != nil {
		t.Fatal(err)
	}
	next := testDeltaState("p", 2, "two")
	apply := ProjectDeltaApply{Project: CatalogProject{ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db"}, Expected: initial, Next: next, Changes: []ProjectDeltaChange{{IssueID: "issue", Issue: &archived, MaterializedIssue: &archived}}}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, store := range []*Store{first, second} {
		go func(store *Store) {
			ready.Done()
			<-start
			errs <- store.ApplyProjectDelta(ctx, apply)
		}(store)
	}
	ready.Wait()
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent idempotent apply: %v", err)
		}
	}
	project := projectSnapshot(t, first, "p")
	if project.DeltaCursor != 2 || len(project.Tasks) != 1 || !project.Tasks[0].State.IsArchived() {
		t.Fatalf("concurrent archived apply did not converge: %+v", project)
	}
}

func TestApplyProjectMaterializedIssuesUpdatesCurrentStateWithoutOrderingItByDeliveryCursor(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	initial := testDeltaState("p", 7, "issue-stream-seven")
	issue := domain.Task{ID: "issue", Title: "bounded", Status: domain.StatusInProgress, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now}
	if err := store.ReplaceProject(ctx, ProjectInput{ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db", Tasks: []domain.Task{issue}, Delta: &initial}); err != nil {
		t.Fatal(err)
	}
	materialized := issue
	materialized.Title = "stale materializer title"
	materialized.Status = domain.StatusOpen
	materialized.Session = &domain.Session{IssueID: issue.ID, State: domain.SessionBusy, Activity: "working", UpdatedAt: now.Add(time.Second)}
	materialized.HasTmuxSession = true
	materialized.Ownership = &domain.IssueOwnership{OwnerID: "agent", OwnerKind: "agent", ClaimedAt: now}
	if err := store.ApplyProjectMaterializedIssues(ctx, "p", []ProjectDeltaChange{{IssueID: issue.ID.String(), Issue: &materialized}}); err != nil {
		t.Fatal(err)
	}
	state, err := store.ProjectDeltaState(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	project := projectSnapshot(t, store, "p")
	if state.Cursor != initial.Cursor || state.Hash != initial.Hash || !reflect.DeepEqual(state.SourceVector, initial.SourceVector) {
		t.Fatalf("current-state apply changed issue delivery component: before=%+v after=%+v", initial, state)
	}
	if len(project.Tasks) != 1 || project.Tasks[0].Title != issue.Title || project.Tasks[0].Status != issue.Status || project.Tasks[0].Session == nil || !project.Tasks[0].HasTmuxSession || project.Tasks[0].Ownership == nil {
		t.Fatalf("current-state materialization was not applied: %+v", project.Tasks)
	}
}

func TestRootUserProjectionSuppressesTerminalSessionAcrossPublicationPaths(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, time.July, 17, 3, 0, 0, 0, time.UTC)
	state := testDeltaState("p", 1, "terminal-bootstrap")
	terminal := domain.Task{
		ID: "closed", Title: "closed", Status: domain.StatusDone, Type: domain.TypeBug,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
		Session:        &domain.Session{IssueID: "closed", State: domain.SessionBusy, Activity: "idle", ActivitySource: "hooks", UpdatedAt: now},
		HasTmuxSession: true,
	}
	if err := store.ReplaceProject(ctx, ProjectInput{ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db", Tasks: []domain.Task{terminal}, Delta: &state}); err != nil {
		t.Fatal(err)
	}
	assertSuppressed := func() {
		t.Helper()
		project := projectSnapshot(t, store, "p")
		if len(project.Tasks) != 1 || project.Tasks[0].Session != nil || project.Tasks[0].HasTmuxSession {
			t.Fatalf("terminal root projection exposed healthy session: %+v", project.Tasks)
		}
	}
	assertSuppressed()
	if err := store.ApplyProjectMaterializedIssues(ctx, "p", []ProjectDeltaChange{{IssueID: terminal.ID.String(), Issue: &terminal}}); err != nil {
		t.Fatal(err)
	}
	assertSuppressed()
}

func TestApplyProjectDeltaAtomicallyAdvancesEmptySourceAndAffectedCurrentIssue(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	initial := testDeltaState("p", 1, "one")
	issue := domain.Task{ID: "issue", Title: "canonical", Status: domain.StatusInReview, Type: domain.TypeInvestigation, CreatedAt: now, UpdatedAt: now}
	if err := store.ReplaceProject(ctx, ProjectInput{ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db", Tasks: []domain.Task{issue}, Delta: &initial}); err != nil {
		t.Fatal(err)
	}
	materialized := issue
	materialized.Facts = domain.DeriveIssueFacts(domain.IssueFactsInput{Status: issue.Status, Type: issue.Type})
	next := testDeltaState("p", 2, "two")
	next.SourceVector[0].SourceTo = "2"
	err := store.ApplyProjectDelta(ctx, ProjectDeltaApply{
		Project: CatalogProject{ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db"}, Expected: initial, Next: next,
		Changes: []ProjectDeltaChange{{IssueID: issue.ID.String(), MaterializedIssue: &materialized}},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.ProjectDeltaState(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	project := projectSnapshot(t, store, "p")
	if state.Cursor != 2 || len(project.Tasks) != 1 || project.Tasks[0].Title != issue.Title || project.Tasks[0].IssueFacts().WaitingHuman {
		t.Fatalf("empty source advance did not atomically materialize affected current issue: state=%+v tasks=%+v", state, project.Tasks)
	}
}

func TestApplyProjectDeltaFailurePreservesLastGoodRowsAndComponent(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	initial := testDeltaState("p", 1, "one")
	old := projectionTestTask("issue", "old")
	if err := store.ReplaceProject(ctx, ProjectInput{ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db", Tasks: []domain.Task{old}, Delta: &initial}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_delta_apply BEFORE INSERT ON project_issue_projection WHEN NEW.title='fail' BEGIN SELECT RAISE(ABORT,'fail'); END`); err != nil {
		t.Fatal(err)
	}
	failed := projectionTestTask("issue", "fail")
	next := testDeltaState("p", 2, "two")
	err := store.ApplyProjectDelta(ctx, ProjectDeltaApply{Project: CatalogProject{ProjectID: "p"}, Expected: initial, Next: next, Changes: []ProjectDeltaChange{{IssueID: "issue", Issue: &failed}}})
	if err == nil {
		t.Fatal("delta apply unexpectedly succeeded")
	}
	state, err := store.ProjectDeltaState(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	project := projectSnapshot(t, store, "p")
	if state.Cursor != 1 || state.Hash != "one" || len(project.Tasks) != 1 || project.Tasks[0].Title != "old" {
		t.Fatalf("failed apply changed last good state: state=%+v project=%+v", state, project)
	}
}

func TestApplyProjectDeltaPreservesIndependentMaterializedFactInputs(t *testing.T) {
	tests := []struct {
		name  string
		type_ domain.TaskType
		facts domain.IssueFacts
		check func(*testing.T, domain.IssueFacts)
	}{
		{
			name:  "active operation blocker",
			type_: domain.TypeTask,
			facts: domain.DeriveIssueFacts(domain.IssueFactsInput{
				Status: domain.StatusOpen, Type: domain.TypeTask,
				OperationBlockers: []domain.IssueOperationBlocker{{OperationID: "op-1", Kind: "session.start", State: "running", BlockedResourceKeys: []string{"issue:p:issue"}}},
			}),
			check: func(t *testing.T, facts domain.IssueFacts) {
				t.Helper()
				if !facts.DelegatedOperation || len(facts.OperationBlockers) != 1 || facts.OperationBlockers[0].OperationID != "op-1" {
					t.Fatalf("operation facts were not preserved: %+v", facts)
				}
			},
		},
		{
			name:  "unresolved interaction",
			type_: domain.TypeTask,
			facts: domain.DeriveIssueFacts(domain.IssueFactsInput{Status: domain.StatusOpen, Type: domain.TypeTask, DecisionWaiting: true, DecisionWaitReason: "choose a strategy"}),
			check: func(t *testing.T, facts domain.IssueFacts) {
				t.Helper()
				if !facts.WaitingHuman || facts.WaitingHumanSource != domain.WaitingHumanSourceInteractionRequest || facts.WaitingHumanReason != "choose a strategy" {
					t.Fatalf("interaction facts were not preserved: %+v", facts)
				}
			},
		},
		{
			name:  "unaccepted human findings investigation",
			type_: domain.TypeInvestigation,
			facts: domain.DeriveIssueFacts(domain.IssueFactsInput{Status: domain.StatusOpen, Type: domain.TypeInvestigation, InvestigationAcceptance: &domain.InvestigationAcceptance{Disposition: domain.InvestigationDispositionHumanFindings, Reason: "findings need acceptance"}}),
			check: func(t *testing.T, facts domain.IssueFacts) {
				t.Helper()
				if !facts.WaitingHuman || facts.WaitingHumanSource != domain.WaitingHumanSourceInvestigationAcceptance || facts.WaitingHumanReason != "findings need acceptance" {
					t.Fatalf("investigation acceptance facts were not preserved: %+v", facts)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := openTestStore(t)
			ctx := context.Background()
			old := projectionTestTask("issue", "before")
			old.Type = tt.type_
			old.Facts = tt.facts
			initial := testDeltaState("p", 1, "one")
			if err := store.ReplaceProject(ctx, ProjectInput{ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db", Tasks: []domain.Task{old}, Delta: &initial}); err != nil {
				t.Fatal(err)
			}

			incoming := domain.CanonicalIssueProjectionTask(old)
			incoming.Title = "covered issue delta"
			incoming.Status = domain.StatusInProgress
			incoming.Facts = domain.IssueFacts{}
			next := testDeltaState("p", 2, "two")
			if err := store.ApplyProjectDelta(ctx, ProjectDeltaApply{
				Project:  CatalogProject{ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db"},
				Expected: initial, Next: next,
				Changes: []ProjectDeltaChange{{IssueID: "issue", Issue: &incoming}},
			}); err != nil {
				t.Fatal(err)
			}
			got := projectSnapshot(t, store, "p").Tasks[0]
			if got.Title != "covered issue delta" || got.Status != domain.StatusInProgress || got.Facts.LifecycleState != domain.IssueWorkflowActive {
				t.Fatalf("incoming issue lifecycle was not re-derived: %+v", got)
			}
			tt.check(t, got.Facts)
		})
	}
}

func TestProjectDeltaComponentsConvergeAcrossInterleavingsAndRestart(t *testing.T) {
	applyOrder := func(t *testing.T, order []string) (protocol.GlobalSnapshotResponseBody, map[string]ProjectDeltaState) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "user.db")
		store, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		for _, projectID := range []string{"a", "b"} {
			initial := testDeltaState(projectID, 1, projectID+"-one")
			if err := store.ReplaceProject(ctx, ProjectInput{ProjectID: projectID, Name: projectID, Path: "/" + projectID, DBPath: "/" + projectID + "/db", Delta: &initial}); err != nil {
				t.Fatal(err)
			}
		}
		for _, projectID := range order {
			initial := testDeltaState(projectID, 1, projectID+"-one")
			next := testDeltaState(projectID, 2, projectID+"-two")
			fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			issue := domain.Task{ID: "same", Title: projectID, Status: domain.StatusOpen, Type: domain.TypeTask, CreatedAt: fixed, UpdatedAt: fixed}
			if err := store.ApplyProjectDelta(ctx, ProjectDeltaApply{Project: CatalogProject{ProjectID: projectID, Name: projectID, Path: "/" + projectID, DBPath: "/" + projectID + "/db"}, Expected: initial, Next: next, Changes: []ProjectDeltaChange{{IssueID: "same", Issue: &issue}}}); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		store, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		snapshot, err := store.Snapshot(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		states := map[string]ProjectDeltaState{}
		for _, projectID := range []string{"a", "b"} {
			states[projectID], err = store.ProjectDeltaState(ctx, projectID)
			if err != nil {
				t.Fatal(err)
			}
		}
		return snapshot, states
	}
	leftSnapshot, leftStates := applyOrder(t, []string{"a", "b"})
	rightSnapshot, rightStates := applyOrder(t, []string{"b", "a"})
	leftSnapshot.GeneratedAt, rightSnapshot.GeneratedAt = time.Time{}, time.Time{}
	for index := range leftSnapshot.Projects {
		leftSnapshot.Projects[index].LastRefreshedAt, leftSnapshot.Projects[index].LastAttemptAt = nil, nil
		rightSnapshot.Projects[index].LastRefreshedAt, rightSnapshot.Projects[index].LastAttemptAt = nil, nil
	}
	if !reflect.DeepEqual(leftSnapshot, rightSnapshot) || !reflect.DeepEqual(leftStates, rightStates) {
		t.Fatalf("interleavings diverged:\nleft=%+v %+v\nright=%+v %+v", leftSnapshot, leftStates, rightSnapshot, rightStates)
	}
}

func TestUserDeltaConsumerMigrationHistoricalUpgradeReopenAndChecksumDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "historical.db")
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`CREATE TABLE schema_migrations(id TEXT PRIMARY KEY,applied_at TEXT NOT NULL,artifact_checksum TEXT);
		CREATE TABLE projects(project_id TEXT PRIMARY KEY,name TEXT NOT NULL,path TEXT NOT NULL,db_path TEXT NOT NULL,schema_version INTEGER NOT NULL DEFAULT 0,projection_version INTEGER NOT NULL,schema_fingerprint TEXT NOT NULL DEFAULT '',checkpoint INTEGER NOT NULL DEFAULT 0,refresh_generation INTEGER NOT NULL DEFAULT 0,freshness TEXT NOT NULL,refreshed_at TEXT,last_attempt_at TEXT,last_error TEXT NOT NULL DEFAULT '',registered INTEGER NOT NULL DEFAULT 1);
		INSERT INTO projects(project_id,name,path,db_path,projection_version,freshness) VALUES('p','P','/p','/p/db',2,'fresh');
		INSERT INTO schema_migrations VALUES
		('user_0001_cross_project_projection','2025-01-01T00:00:00Z','a313332cc21b8c02be4125bfddc9a05299d41b4dc76414abe51163ae88f97d41'),
		('user_0002_normalized_projection','2025-01-01T00:00:00Z','15a9ef67dd84425a0d29ab62f7107755134799567b6671b477782467496c5434'),
		('user_0003_canonical_issue_state_repair','2025-01-01T00:00:00Z','981bf427d53fe031296d27659293494c48c63f8865333a08340a0fe542c4883f'),
		('user_0004_canonical_archive_state_repair','2025-01-01T00:00:00Z','302f16948cea6ddef8ea11e3a6fac09f9234817e9d3e29d9b9b516f707e83941')`)
	if err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var cursor uint64
	var checksum string
	if err := store.db.QueryRow(`SELECT delta_cursor FROM projects WHERE project_id='p'`).Scan(&cursor); err != nil || cursor != 0 {
		t.Fatalf("historical cursor=%d err=%v", cursor, err)
	}
	if err := store.db.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id='user_0005_project_delta_consumer'`).Scan(&checksum); err != nil || checksum != "3462d998e1abfb9ef02f22b964934bbcd7b6e2c9e25e233a2d9d89002f6cc863" {
		t.Fatalf("migration checksum=%q err=%v", checksum, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("idempotent reopen: %v", err)
	}
	if _, err := reopened.db.Exec(`UPDATE schema_migrations SET artifact_checksum='drift' WHERE id='user_0005_project_delta_consumer'`); err != nil {
		t.Fatal(err)
	}
	_ = reopened.Close()
	if _, err := Open(path); err == nil {
		t.Fatal("checksum drift unexpectedly opened")
	}
}

func TestUserDeltaConsumerMigrationRepairsMissingColumnWithAppliedLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drift.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`ALTER TABLE projects DROP COLUMN delta_hash`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("repair missing applied-migration column: %v", err)
	}
	defer reopened.Close()
	var columns, markers int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('projects') WHERE name='delta_hash'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id='user_0005_project_delta_consumer' AND artifact_checksum='3462d998e1abfb9ef02f22b964934bbcd7b6e2c9e25e233a2d9d89002f6cc863'`).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if columns != 1 || markers != 1 {
		t.Fatalf("repaired columns=%d markers=%d", columns, markers)
	}
}

func TestUserDeltaConsumerMigrationRejectsColumnContractDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contract-drift.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`ALTER TABLE projects DROP COLUMN delta_hash; ALTER TABLE projects ADD COLUMN delta_hash INTEGER NOT NULL DEFAULT 0`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("column contract drift unexpectedly opened")
	}
}
