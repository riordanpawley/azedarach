package userstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestNormalizedProjectionRoundTripsViewAndSearchFields(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "user.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	estimate := 8
	parent := naming.IssueID("parent")
	started := now.Add(-time.Hour)
	expires := now.Add(time.Hour)
	task := domain.Task{ID: "child", Title: "Needle title", Description: "description token", Notes: "notes token", Design: "design token", Acceptance: "acceptance token", Assignee: "agent", Labels: []string{"label-token"}, Estimate: &estimate, Status: domain.StatusInReview, Priority: domain.P1, Type: domain.TypeFeature, ParentID: &parent, Dependencies: []domain.Dependency{{ID: "blocker", Type: domain.DependencyBlockedBy}}, Implementations: []string{"default", "alt"}, Session: &domain.Session{IssueID: "child", Role: "worker", ScopeKind: "issue", ScopeID: "child", State: domain.SessionIdle, Activity: "idle", ActivitySource: "hook", TotalCount: 2, ActiveCount: 1, PausedCount: 1, TmuxAttached: true, TmuxAttachedCount: 2, StartedAt: &started, UpdatedAt: now, Worktree: "/work", DevServer: &domain.DevServer{Port: 8080, Command: "serve", Running: true}}, HasTmuxSession: true, HasWorktree: true, GitAheadCount: 2, GitBehindCount: 1, HasUncommittedChanges: true, HasConflicts: true, ConflictFiles: []string{"a.go"}, GitAdditions: 10, GitDeletions: 4, Origin: "origin", PullRequest: &domain.PullRequest{Number: 42, RemoteKey: "r", DisplayKey: "#42", URL: "https://example.test/42", State: "open", Draft: true, ChecksStatus: "passing"}, RuntimeUpdatedAt: now, Ownership: &domain.IssueOwnership{OwnerID: "owner", OwnerKind: "agent", ClaimedAt: now, ExpiresAt: &expires}, CoordinationLeases: []domain.CoordinationLease{{Purpose: domain.CoordinationLeaseReview, OwnerID: "reviewer", OwnerKind: "agent", ClaimedAt: now}}, CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now}
	if err = store.ReplaceProject(ctx, ProjectInput{ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db", Checkpoint: 9, Tasks: []domain.Task{task}}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"Needle", "description token", "notes token", "design token", "acceptance token", "label-token"} {
		snap, e := store.Snapshot(ctx, query)
		if e != nil {
			t.Fatal(e)
		}
		if len(snap.Projects[0].Tasks) != 1 {
			t.Fatalf("query %q returned %#v", query, snap.Projects[0].Tasks)
		}
	}
	snap, err := store.Snapshot(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	got := snap.Projects[0].Tasks[0]
	if got.Description != task.Description || got.Notes != task.Notes || got.Design != task.Design || got.Acceptance != task.Acceptance || !reflect.DeepEqual(got.Labels, task.Labels) || !reflect.DeepEqual(got.Implementations, task.Implementations) {
		t.Fatalf("normalized issue mismatch: %#v", got)
	}
	if got.Session == nil || got.Session.ActivitySource != "hook" || got.Session.DevServer == nil || got.Session.DevServer.Port != 8080 || !got.HasWorktree || !reflect.DeepEqual(got.ConflictFiles, task.ConflictFiles) {
		t.Fatalf("runtime mismatch: %#v", got)
	}
	if len(got.Dependencies) != 1 || got.Dependencies[0] != task.Dependencies[0] || got.ParentID == nil || *got.ParentID != parent {
		t.Fatalf("relations mismatch: %#v", got)
	}
	if got.PullRequest == nil || got.PullRequest.Number != 42 || got.Ownership == nil || got.Ownership.OwnerID != "owner" || len(got.CoordinationLeases) != 1 {
		t.Fatalf("metadata mismatch: %#v", got)
	}
	view := domain.CloseoutBoardView()
	filtered, err := store.SnapshotForView(ctx, "", &view)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Projects[0].Tasks) != 1 {
		t.Fatalf("review view lost normalized task: %#v", filtered)
	}
}

func TestSnapshotForScopedViewHydratesExcludedTasksByScopedIdentity(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "user.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	for _, project := range []ProjectInput{
		{ProjectID: "alpha", Name: "Alpha", Path: "/alpha", Tasks: []domain.Task{{ID: "same", Title: "Alpha excluded", Status: domain.StatusOpen, Type: domain.TypeBug, CreatedAt: now, UpdatedAt: now}, {ID: "active", Title: "Alpha active", Status: domain.StatusInProgress, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now}}},
		{ProjectID: "beta", Name: "Beta", Path: "/beta", Tasks: []domain.Task{{ID: "same", Title: "Beta excluded", Status: domain.StatusOpen, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now}}},
	} {
		if err = store.ReplaceProject(ctx, project); err != nil {
			t.Fatal(err)
		}
	}
	view := domain.OrchestrationBoardView()
	snapshot, err := store.SnapshotForScopedViewWithTasks(ctx, "", &view, protocol.GlobalViewScope{}, []protocol.ScopedIssueID{{ProjectID: "alpha", IssueID: "same"}})
	if err != nil {
		t.Fatal(err)
	}
	byProject := make(map[string][]domain.Task)
	for _, project := range snapshot.Projects {
		byProject[project.ProjectID] = project.Tasks
	}
	if got := byProject["alpha"]; len(got) != 2 || got[0].ID != "active" || got[1].ID != "same" || got[1].Title != "Alpha excluded" {
		t.Fatalf("alpha hydrated tasks = %#v", got)
	}
	if got := byProject["beta"]; len(got) != 0 {
		t.Fatalf("beta duplicate was hydrated by bare ID: %#v", got)
	}
}

func TestSnapshotForScopedViewHydratesProjectOutsideConfiguredScope(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "user.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	for _, project := range []ProjectInput{
		{ProjectID: "alpha", Name: "Alpha", Path: "/alpha", Tasks: []domain.Task{{ID: "active", Title: "Visible", Status: domain.StatusInProgress, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now}}},
		{ProjectID: "beta", Name: "Beta", Path: "/beta", Tasks: []domain.Task{{ID: "live", Title: "Outside scope", Status: domain.StatusOpen, Type: domain.TypeBug, CreatedAt: now, UpdatedAt: now}}},
	} {
		if err = store.ReplaceProject(ctx, project); err != nil {
			t.Fatal(err)
		}
	}
	view := domain.OrchestrationBoardView()
	snapshot, err := store.SnapshotForScopedViewWithTasks(ctx, "", &view, protocol.GlobalViewScope{Kind: protocol.GlobalViewScopeSelectedProjects, ProjectIDs: []naming.ProjectID{"alpha"}}, []protocol.ScopedIssueID{{ProjectID: "beta", IssueID: "live"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Projects) != 2 {
		t.Fatalf("metadata projects = %+v, want scoped alpha plus hydrated beta", snapshot.Projects)
	}
	for _, project := range snapshot.Projects {
		if project.ProjectID == "beta" && (len(project.Tasks) != 1 || project.Tasks[0].Title != "Outside scope") {
			t.Fatalf("hydrated beta = %+v", project)
		}
	}
}

func TestSnapshotForScopedViewWithTasksAllowsQueryWithoutFTSExpression(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "user.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	if err = store.ReplaceProject(ctx, ProjectInput{ProjectID: "p", Name: "P", Path: "/p", Tasks: []domain.Task{{ID: "active", Title: "Active", Status: domain.StatusInProgress, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now}}}); err != nil {
		t.Fatal(err)
	}
	view := domain.OrchestrationBoardView()
	snapshot, err := store.SnapshotForScopedViewWithTasks(ctx, `""`, &view, protocol.GlobalViewScope{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Projects) != 1 || len(snapshot.Projects[0].Tasks) != 1 {
		t.Fatalf("snapshot = %+v, want active view candidate", snapshot)
	}
}

func TestOlderCheckpointCannotOverwriteNewerProjectionAcrossStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "user.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	if err = first.ReplaceProject(ctx, ProjectInput{ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db", Checkpoint: 20, Tasks: []domain.Task{{ID: "new", Title: "new", Status: domain.StatusOpen, UpdatedAt: now}}}); err != nil {
		t.Fatal(err)
	}
	if err = second.ReplaceProject(ctx, ProjectInput{ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db", Checkpoint: 19, Tasks: []domain.Task{{ID: "old", Title: "old", Status: domain.StatusOpen, UpdatedAt: now}}}); err != nil {
		t.Fatal(err)
	}
	snap, err := first.Snapshot(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Projects[0].Checkpoint != 20 || len(snap.Projects[0].Tasks) != 1 || snap.Projects[0].Tasks[0].ID != "new" {
		t.Fatalf("stale projection overwrote newer generation: %#v", snap.Projects[0])
	}
}

func TestReconcileProjectsUpdatesCatalogWithoutDroppingLastGoodRows(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "user.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	if err = store.ReplaceProject(ctx, ProjectInput{ProjectID: "p", Name: "old", Path: "/old", DBPath: "/old/db", Checkpoint: 1, Tasks: []domain.Task{{ID: "i", Title: "kept", Status: domain.StatusOpen, UpdatedAt: now}}}); err != nil {
		t.Fatal(err)
	}
	if err = store.ReconcileProjects(ctx, []CatalogProject{{ProjectID: "p", Name: "new", Path: "/new", DBPath: "/new/db"}, {ProjectID: "q", Name: "Q", Path: "/q", DBPath: "/q/db"}}); err != nil {
		t.Fatal(err)
	}
	snap, err := store.Snapshot(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var renamed *protocol.GlobalProjectSnapshot
	for i := range snap.Projects {
		if snap.Projects[i].ProjectID == "p" {
			renamed = &snap.Projects[i]
		}
	}
	if len(snap.Projects) != 2 || renamed == nil || renamed.Name != "new" || len(renamed.Tasks) != 1 {
		t.Fatalf("catalog reconcile=%#v", snap.Projects)
	}
	if err = store.ReconcileProjects(ctx, []CatalogProject{{ProjectID: "q", Name: "Q", Path: "/q", DBPath: "/q/db"}}); err != nil {
		t.Fatal(err)
	}
	snap, _ = store.Snapshot(ctx, "")
	for _, p := range snap.Projects {
		if p.ProjectID == "p" && (p.Registered || p.Freshness != protocol.GlobalProjectionFreshnessUnavailable) {
			t.Fatalf("removed project health=%#v", p)
		}
	}
}

func TestGlobalViewScopeRoundTrip(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "user.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	view := domain.DefaultBoardView()
	view.ID = "scoped"
	view.Title = "Scoped"
	record, err := store.SaveGlobalView(ctx, protocol.GlobalViewRecord{View: view, Scope: protocol.GlobalViewScope{Kind: protocol.GlobalViewScopeSelectedProjects, ProjectIDs: []naming.ProjectID{"p", "q"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Scope.ProjectIDs) != 2 {
		t.Fatalf("saved scope=%#v", record.Scope)
	}
	resolved, err := store.ResolveGlobalView(ctx, "scoped", "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resolved.Scope.ProjectIDs, []naming.ProjectID{"p", "q"}) {
		t.Fatalf("resolved=%#v", resolved)
	}
	listed, err := store.ListGlobalViews(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range listed {
		if item.View.ID == "scoped" {
			found = true
			if item.Scope.Kind != protocol.GlobalViewScopeSelectedProjects {
				t.Fatalf("listed=%#v", item)
			}
		}
	}
	if !found {
		t.Fatal("scoped view not listed")
	}
}

func TestDeleteGlobalViewResetsConsumerSelections(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "user.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	view := domain.DefaultBoardView()
	view.ID, view.Title = "custom-selected", "Custom selected"
	if _, err := store.SaveGlobalView(ctx, protocol.GlobalViewRecord{View: view, Scope: protocol.GlobalViewScope{Kind: protocol.GlobalViewScopeAllProjects}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SelectView(ctx, string(protocol.GlobalViewConsumerTmuxSelector), string(view.ID)); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteView(ctx, string(view.ID)); err != nil {
		t.Fatal(err)
	}
	selections, err := store.ListViewSelections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := selections[protocol.GlobalViewConsumerTmuxSelector]; got != string(domain.BoardViewOrchestrationID) {
		t.Fatalf("selector fallback = %q", got)
	}
}

func TestOpenEnablesSQLiteSafetyPragmas(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "user.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for pragma, want := range map[string]int{"foreign_keys": 1, "busy_timeout": 5000} {
		var got int
		if err = store.db.QueryRow(`PRAGMA ` + pragma).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s=%d want %d", pragma, got, want)
		}
	}
}

func TestMigrationPreservesLegacyProjectionAndUnrelatedUserData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "user.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE preferences(key TEXT PRIMARY KEY,value TEXT NOT NULL); INSERT INTO preferences VALUES('theme','dark');
CREATE TABLE project_issue_projection(project_id TEXT NOT NULL,issue_id TEXT NOT NULL,task_json BLOB NOT NULL,title TEXT NOT NULL,status TEXT NOT NULL,lifecycle TEXT NOT NULL,display_phase TEXT NOT NULL,closed_outcome TEXT NOT NULL,review_ready INTEGER NOT NULL DEFAULT 0,waiting_human INTEGER NOT NULL DEFAULT 0,waiting_ai INTEGER NOT NULL DEFAULT 0,human_attention_rank INTEGER NOT NULL DEFAULT 0,priority INTEGER NOT NULL,issue_type TEXT NOT NULL,parent_issue_id TEXT NOT NULL DEFAULT '',git_diff_total INTEGER NOT NULL DEFAULT 0,session_rank INTEGER NOT NULL DEFAULT 0,updated_at TEXT NOT NULL,PRIMARY KEY(project_id,issue_id));`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	raw, err := json.Marshal(domain.Task{ID: "legacy", Title: "legacy title", Description: "legacy description", Status: domain.StatusOpen, Type: domain.TypeTask, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO project_issue_projection(project_id,issue_id,task_json,title,status,lifecycle,display_phase,closed_outcome,priority,issue_type,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, "p", "legacy", raw, "legacy title", "open", "open", "open", "none", 2, "task", now.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var preference string
	if err = store.db.QueryRow(`SELECT value FROM preferences WHERE key='theme'`).Scan(&preference); err != nil || preference != "dark" {
		t.Fatalf("unrelated data=%q err=%v", preference, err)
	}
	var taskJSONColumns int
	if err = store.db.QueryRow(`SELECT count(*) FROM pragma_table_info('project_issue_projection') WHERE name='task_json'`).Scan(&taskJSONColumns); err != nil {
		t.Fatal(err)
	}
	if taskJSONColumns != 0 {
		t.Fatal("legacy task_json column remains")
	}
	// Catalog metadata may be refreshed independently; seed it without touching the migrated row.
	if err = store.ReconcileProjects(context.Background(), []CatalogProject{{ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db"}}); err != nil {
		t.Fatal(err)
	}
	snap, err := store.Snapshot(context.Background(), "legacy description")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Projects) != 1 || len(snap.Projects[0].Tasks) != 1 || snap.Projects[0].Tasks[0].Description != "legacy description" {
		t.Fatalf("migrated snapshot=%#v", snap)
	}
}

func TestArchivedIssueRemainsQueryable(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "user.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err := domain.NewIssueState(domain.IssueStateParts{Workflow: domain.IssueWorkflowClosed, Review: domain.IssueReviewNone, CloseOutcome: domain.IssueCloseCompleted, Archive: domain.IssueArchiveArchived})
	if err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "archived", Title: "archived searchable", Status: domain.StatusDone, State: state, Type: domain.TypeTask, UpdatedAt: time.Now().UTC()}
	if err = store.ReplaceProject(context.Background(), ProjectInput{ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db", Tasks: []domain.Task{task}}); err != nil {
		t.Fatal(err)
	}
	snap, err := store.Snapshot(context.Background(), "archived searchable")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Projects[0].Tasks) != 1 || snap.Projects[0].Tasks[0].State.Archive() != domain.IssueArchiveArchived {
		t.Fatalf("archived task=%#v", snap.Projects[0].Tasks)
	}
}

func TestRefreshGenerationRejectsEqualCheckpointStaleRuntimeWriter(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "user.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	project := CatalogProject{ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db"}
	older, err := store.BeginProjectRefresh(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	newer, err := store.BeginProjectRefresh(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err = store.ReplaceProject(ctx, ProjectInput{ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db", Checkpoint: 5, RefreshGeneration: newer, Tasks: []domain.Task{{ID: "new-runtime", Title: "new", Status: domain.StatusOpen, UpdatedAt: now}}}); err != nil {
		t.Fatal(err)
	}
	if err = store.ReplaceProject(ctx, ProjectInput{ProjectID: "p", Name: "stale", Path: "/stale", DBPath: "/stale/db", Checkpoint: 5, RefreshGeneration: older, Tasks: []domain.Task{{ID: "old-runtime", Title: "old", Status: domain.StatusOpen, UpdatedAt: now}}}); err != nil {
		t.Fatal(err)
	}
	if err = store.MarkUnavailableGeneration(ctx, "p", "stale", "/stale", "/stale/db", older, errors.New("late failure")); err != nil {
		t.Fatal(err)
	}
	snap, err := store.Snapshot(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	p := snap.Projects[0]
	if p.Name != "P" || p.Freshness != protocol.GlobalProjectionFreshnessFresh || len(p.Tasks) != 1 || p.Tasks[0].ID != "new-runtime" {
		t.Fatalf("stale generation published: %#v", p)
	}
}

func TestSnapshotAgesFreshProjectionToStaleAndPreservesLastGoodTasks(t *testing.T) {
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	store, err := Open(filepath.Join(t.TempDir(), "user.db"), WithMaxProjectionAge(time.Minute), withClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err = store.ReplaceProject(ctx, ProjectInput{ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db", Tasks: []domain.Task{{ID: "kept", Title: "kept", Status: domain.StatusOpen, UpdatedAt: now}}}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	snap, err := store.Snapshot(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	p := snap.Projects[0]
	if !snap.Partial || p.Freshness != protocol.GlobalProjectionFreshnessStale || p.LastAttemptAt == nil || p.LastRefreshedAt == nil || len(p.Tasks) != 1 {
		t.Fatalf("aged snapshot=%#v", snap)
	}
}

func TestReviewPredicateUsesHydratedSessionState(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "user.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	tasks := []domain.Task{{ID: "idle", Title: "idle", Status: domain.StatusInReview, Type: domain.TypeTask, Session: &domain.Session{IssueID: "idle", State: domain.SessionIdle, Activity: "idle", UpdatedAt: now}, HasTmuxSession: true, UpdatedAt: now}, {ID: "busy", Title: "busy", Status: domain.StatusInReview, Type: domain.TypeTask, Session: &domain.Session{IssueID: "busy", State: domain.SessionBusy, Activity: "working", UpdatedAt: now}, HasTmuxSession: true, UpdatedAt: now}}
	if err = store.ReplaceProject(context.Background(), ProjectInput{ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db", Tasks: tasks}); err != nil {
		t.Fatal(err)
	}
	view := domain.CloseoutBoardView()
	snap, err := store.SnapshotForView(context.Background(), "", &view)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Projects[0].Tasks) != 1 || snap.Projects[0].Tasks[0].ID != "idle" || !snap.Projects[0].Tasks[0].IssueFacts().ReviewReadyVisible {
		t.Fatalf("review tasks=%#v", snap.Projects[0].Tasks)
	}
}

func TestFTSSearchIsProjectScopedAndUsesVirtualIndex(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "user.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	for _, project := range []struct{ id, title string }{{"a", "needle alpha"}, {"b", "needle beta"}} {
		if err = store.ReplaceProject(ctx, ProjectInput{ProjectID: project.id, Name: project.id, Path: "/" + project.id, DBPath: "/" + project.id + "/db", Tasks: []domain.Task{{ID: "same", Title: project.title, Status: domain.StatusOpen, Type: domain.TypeTask, UpdatedAt: now}}}); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := store.Snapshot(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range snap.Projects {
		if p.ProjectID == "a" && len(p.Tasks) != 1 {
			t.Fatalf("project a=%#v", p.Tasks)
		}
		if p.ProjectID == "b" && len(p.Tasks) != 0 {
			t.Fatalf("cross-project FTS collision=%#v", p.Tasks)
		}
	}
	rows, err := store.db.Query(`EXPLAIN QUERY PLAN SELECT issue_id FROM project_issue_search_projection WHERE project_id=? AND project_issue_search_projection MATCH ?`, "a", domain.ContentQueryFTSExpression("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	usedVirtual := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(detail), "virtual table index") {
			usedVirtual = true
		}
	}
	if !usedVirtual {
		t.Fatal("FTS query plan did not use virtual index")
	}
}

func TestReplaceProjectScopesCollidingIssueIDsAndIsIdempotent(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "root.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	id := naming.IssueID("same")
	for _, in := range []ProjectInput{{ProjectID: "p-a", Name: "A", Path: "/a", DBPath: "/a/db", Tasks: []domain.Task{{ID: id, Title: "alpha", UpdatedAt: now}}}, {ProjectID: "p-b", Name: "B", Path: "/b", DBPath: "/b/db", Tasks: []domain.Task{{ID: id, Title: "beta", UpdatedAt: now}}}} {
		if err := store.ReplaceProject(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ReplaceProject(ctx, ProjectInput{ProjectID: "p-a", Name: "A", Path: "/a", DBPath: "/a/db", Tasks: []domain.Task{{ID: id, Title: "alpha", UpdatedAt: now}}}); err != nil {
		t.Fatal(err)
	}
	snap, err := store.Snapshot(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Projects) != 2 || len(snap.Projects[0].Tasks) != 1 || len(snap.Projects[1].Tasks) != 1 {
		t.Fatalf("snapshot=%+v", snap)
	}
}

func TestUnavailableProjectRemainsVisibleAndCatalogRemovalRetainsHealth(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "root.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.MarkUnavailable(ctx, "p-a", "A", "/a", "/a/db", errors.New("offline")); err != nil {
		t.Fatal(err)
	}
	snap, err := store.Snapshot(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Partial || len(snap.Projects) != 1 || snap.Projects[0].Freshness != protocol.GlobalProjectionFreshnessUnavailable {
		t.Fatalf("snapshot=%+v", snap)
	}
	if err := store.ReconcileCatalog(ctx, nil); err != nil {
		t.Fatal(err)
	}
	snap, err = store.Snapshot(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Projects) != 1 || snap.Projects[0].Registered {
		t.Fatalf("projects=%+v", snap.Projects)
	}
}

func TestSnapshotForViewUsesMaterializedSQLitePredicates(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "user.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	tasks := []domain.Task{{ID: "open", Title: "Open", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now}, {ID: "review", Title: "Review", Status: domain.StatusInReview, Priority: domain.P1, Type: domain.TypeFeature, CreatedAt: now, UpdatedAt: now}}
	if err := store.ReplaceProject(ctx, ProjectInput{ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db", Tasks: tasks}); err != nil {
		t.Fatal(err)
	}
	view := domain.CloseoutBoardView()
	snap, err := store.SnapshotForView(ctx, "", &view)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Projects) != 1 || len(snap.Projects[0].Tasks) != 1 || snap.Projects[0].Tasks[0].ID != "review" {
		t.Fatalf("tasks=%+v", snap.Projects[0].Tasks)
	}
}
