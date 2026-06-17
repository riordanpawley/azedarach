package tmuxselector

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

type fakeSessionInventory struct {
	infos   []tmux.SessionInfo
	err     error
	current string
}

func (f fakeSessionInventory) ListSessionInfos(context.Context) ([]tmux.SessionInfo, error) {
	return f.infos, f.err
}

func (f fakeSessionInventory) CurrentSession(context.Context) (string, error) {
	return f.current, nil
}

type fakeProjectSnapshotSource struct {
	snapshots []ProjectInventorySnapshot
}

func (f fakeProjectSnapshotSource) ListProjectSnapshots(context.Context) ([]ProjectInventorySnapshot, error) {
	return append([]ProjectInventorySnapshot(nil), f.snapshots...), nil
}

type fakeTaskSnapshotReader struct {
	tasksByID     map[string]domain.Task
	ancestorCalls [][]string
	leanCalls     [][]string
	metadataCalls [][]string
	listCalls     int
}

func (f *fakeTaskSnapshotReader) GetManyTaskSnapshotWithAncestorsNoDependents(_ context.Context, taskIDs []string) (daemonclient.TaskSnapshot, error) {
	f.leanCalls = append(f.leanCalls, append([]string(nil), taskIDs...))
	tasks := make([]domain.Task, 0, len(f.tasksByID))
	for _, task := range f.tasksByID {
		tasks = append(tasks, task)
	}
	return daemonclient.TaskSnapshot{Tasks: tasks}, nil
}

func (f *fakeTaskSnapshotReader) GetManyTaskSnapshotWithAncestorsNoDependentsMetadataOnly(ctx context.Context, taskIDs []string) (daemonclient.TaskSnapshot, error) {
	f.metadataCalls = append(f.metadataCalls, append([]string(nil), taskIDs...))
	return f.GetManyTaskSnapshotWithAncestorsNoDependents(ctx, taskIDs)
}

func (f *fakeTaskSnapshotReader) GetManyTaskSnapshotWithAncestors(_ context.Context, taskIDs []string) (daemonclient.TaskSnapshot, error) {
	f.ancestorCalls = append(f.ancestorCalls, append([]string(nil), taskIDs...))
	tasks := make([]domain.Task, 0, len(f.tasksByID))
	for _, task := range f.tasksByID {
		tasks = append(tasks, task)
	}
	return daemonclient.TaskSnapshot{Tasks: tasks}, nil
}

func (f *fakeTaskSnapshotReader) ListTasksSnapshot(context.Context) (daemonclient.TaskSnapshot, error) {
	f.listCalls++
	tasks := make([]domain.Task, 0, len(f.tasksByID))
	for _, task := range f.tasksByID {
		tasks = append(tasks, task)
	}
	return daemonclient.TaskSnapshot{Tasks: tasks}, nil
}

func TestGlobalInventoryLoaderUsesTmuxFirstAcrossProjects(t *testing.T) {
	started := time.Unix(1775209200, 0).UTC()
	projectDir := t.TempDir()
	projectID := projectIDForPath(projectDir)
	sessionID := naming.CanonicalSessionID(projectID, "bxo")
	worktree := projectDir + "/worktrees/bxo"
	loader := NewGlobalInventoryLoader(
		fakeSessionInventory{infos: []tmux.SessionInfo{
			{Name: sessionID, CreatedAt: &started, Path: worktree},
			{Name: "az-bxk", Path: "/tmp/other/bxk"},
			{Name: "plain-tmux"},
		}},
		nil,
		WithProjectDirs(projectDir),
		WithProjectSnapshotSource(fakeProjectSnapshotSource{
			snapshots: []ProjectInventorySnapshot{
				{
					ProjectID:   projectID,
					ProjectPath: projectDir,
					Tasks: []domain.Task{{
						ID:       "bxo",
						Title:    "Global session inventory",
						Status:   domain.StatusInProgress,
						Priority: domain.P1,
						Type:     domain.TypeTask,
						Session: &domain.Session{
							IssueID:   "bxo",
							State:     domain.SessionPaused,
							StartedAt: &started,
							Worktree:  worktree,
						},
						HasTmuxSession:        true,
						HasWorktree:           true,
						GitAheadCount:         2,
						HasUncommittedChanges: true,
					}},
				},
			},
		}),
	)

	snapshot, err := loader.ListTasksSnapshot(context.Background())
	if err != nil {
		t.Fatalf("ListTasksSnapshot: %v", err)
	}
	if len(snapshot.Entries) != 3 {
		t.Fatalf("entries = %#v, want 3 tmux sessions", snapshot.Entries)
	}
	first := snapshot.Entries[0]
	if first.SessionID != sessionID || first.IssueID != "bxo" || first.ProjectID != projectID {
		t.Fatalf("first entry = %#v", first)
	}
	if first.Worktree != worktree || !first.HasWorktree || first.State != domain.SessionPaused {
		t.Fatalf("first runtime metadata = %#v", first)
	}
	if first.TaskTitle != "Global session inventory" || first.GitAheadCount != 2 || !first.HasUncommittedChanges {
		t.Fatalf("first task metadata = %#v", first)
	}
	if snapshot.Entries[1].SessionID != "az-bxk" || snapshot.Entries[1].IssueID != "bxk" {
		t.Fatalf("az-prefixed fallback entry = %#v", snapshot.Entries[1])
	}
	if snapshot.Entries[2].SessionID != "plain-tmux" || snapshot.Entries[2].IssueID != "" {
		t.Fatalf("plain tmux fallback entry = %#v", snapshot.Entries[2])
	}
	if len(snapshot.Tasks) != len(snapshot.Entries) {
		t.Fatalf("tasks = %d, entries = %d", len(snapshot.Tasks), len(snapshot.Entries))
	}
}

func TestGlobalInventoryLoaderClosedProjectedTaskKeepsLiveSessionState(t *testing.T) {
	started := time.Unix(1775209200, 0).UTC()
	projectDir := t.TempDir()
	projectID := projectIDForPath(projectDir)
	sessionID := naming.CanonicalSessionID(projectID, "cjf")
	loader := NewGlobalInventoryLoader(
		fakeSessionInventory{infos: []tmux.SessionInfo{
			{Name: sessionID, CreatedAt: &started, Path: projectDir + "/worktrees/cjf"},
		}},
		nil,
		WithProjectDirs(projectDir),
		WithProjectSnapshotSource(fakeProjectSnapshotSource{
			snapshots: []ProjectInventorySnapshot{{
				ProjectID:   projectID,
				ProjectPath: projectDir,
				Tasks: []domain.Task{{
					ID:       "cjf",
					Title:    "Completed issue with live shell",
					Status:   domain.StatusDone,
					Priority: domain.P1,
					Type:     domain.TypeTask,
					Session: &domain.Session{
						IssueID:   "cjf",
						State:     domain.SessionBusy,
						StartedAt: &started,
					},
					HasTmuxSession: true,
					HasWorktree:    true,
				}},
			}},
		}),
	)

	snapshot, err := loader.ListTasksSnapshot(context.Background())
	if err != nil {
		t.Fatalf("ListTasksSnapshot: %v", err)
	}
	if len(snapshot.Entries) != 1 {
		t.Fatalf("entries = %#v, want one", snapshot.Entries)
	}
	if got := snapshot.Entries[0].State; got != domain.SessionBusy {
		t.Fatalf("entry state = %s, want %s", got, domain.SessionBusy)
	}
	if got := snapshot.Tasks[0].Session.State; got != domain.SessionBusy {
		t.Fatalf("task session state = %s, want %s", got, domain.SessionBusy)
	}
}

func TestGlobalInventoryLoaderCarriesHookActivityFromProjection(t *testing.T) {
	started := time.Unix(1775209200, 0).UTC()
	projectDir := t.TempDir()
	projectID := projectIDForPath(projectDir)
	sessionID := naming.CanonicalSessionID(projectID, "cmd")
	loader := NewGlobalInventoryLoader(
		fakeSessionInventory{infos: []tmux.SessionInfo{
			{Name: sessionID, CreatedAt: &started, Path: projectDir + "/worktrees/cmd"},
		}},
		nil,
		WithProjectDirs(projectDir),
		WithProjectSnapshotSource(fakeProjectSnapshotSource{
			snapshots: []ProjectInventorySnapshot{{
				ProjectID:   projectID,
				ProjectPath: projectDir,
				Tasks: []domain.Task{{
					ID:     "cmd",
					Title:  "Idle hook activity",
					Status: domain.StatusInProgress,
					Session: &domain.Session{
						IssueID:        "cmd",
						State:          domain.SessionBusy,
						Activity:       "idle",
						ActivitySource: "hooks",
						StartedAt:      &started,
					},
					HasTmuxSession: true,
				}},
			}},
		}),
	)

	snapshot, err := loader.ListTasksSnapshot(context.Background())
	if err != nil {
		t.Fatalf("ListTasksSnapshot: %v", err)
	}
	if len(snapshot.Entries) != 1 {
		t.Fatalf("entries = %#v, want one", snapshot.Entries)
	}
	entry := snapshot.Entries[0]
	if entry.State != domain.SessionBusy || entry.Activity != "idle" || entry.ActivitySource != "hooks" {
		t.Fatalf("entry activity = state:%s activity:%s source:%s, want busy/idle/hooks", entry.State, entry.Activity, entry.ActivitySource)
	}
	if got := snapshot.Tasks[0].Session.Activity; got != "idle" {
		t.Fatalf("task session activity = %q, want idle", got)
	}
}

func TestGlobalInventoryLoaderCarriesTreeTasksForAncestorRendering(t *testing.T) {
	started := time.Unix(1775209200, 0).UTC()
	projectDir := t.TempDir()
	projectID := projectIDForPath(projectDir)
	rootID := naming.IssueID("az-root")
	childID := naming.IssueID("az-child")
	sessionID := naming.CanonicalSessionID(projectID, childID.String())
	loader := NewGlobalInventoryLoader(
		fakeSessionInventory{infos: []tmux.SessionInfo{
			{Name: sessionID, CreatedAt: &started, Path: projectDir + "/worktrees/" + childID.String()},
		}},
		nil,
		WithProjectDirs(projectDir),
		WithProjectSnapshotSource(fakeProjectSnapshotSource{
			snapshots: []ProjectInventorySnapshot{{
				ProjectID:   projectID,
				ProjectPath: projectDir,
				Tasks: []domain.Task{
					{
						ID:     rootID,
						Title:  "Root orchestration",
						Status: domain.StatusInProgress,
						Type:   domain.TypeEpic,
					},
					{
						ID:       childID,
						Title:    "Worker session",
						Status:   domain.StatusInProgress,
						ParentID: &rootID,
						Session: &domain.Session{
							IssueID:   childID,
							State:     domain.SessionBusy,
							StartedAt: &started,
						},
						HasTmuxSession: true,
					},
				},
			}},
		}),
	)

	snapshot, err := loader.ListTasksSnapshot(context.Background())
	if err != nil {
		t.Fatalf("ListTasksSnapshot: %v", err)
	}
	if len(snapshot.TreeTasks) != 1 {
		t.Fatalf("tree tasks = %#v, want root ancestor task only", snapshot.TreeTasks)
	}
	if snapshot.TreeTasks[0].ID != rootID {
		t.Fatalf("tree tasks = %#v, want root ancestor task", snapshot.TreeTasks)
	}
}

func TestGlobalInventoryLoaderScopesTreeAncestorsByProject(t *testing.T) {
	started := time.Unix(1775209200, 0).UTC()
	projectDir := t.TempDir()
	otherProjectDir := t.TempDir()
	projectID := projectIDForPath(projectDir)
	otherProjectID := projectIDForPath(otherProjectDir)
	rootID := naming.IssueID("az-root")
	childID := naming.IssueID("az-child")
	sessionID := naming.CanonicalSessionID(projectID, childID.String())
	loader := NewGlobalInventoryLoader(
		fakeSessionInventory{infos: []tmux.SessionInfo{
			{Name: sessionID, CreatedAt: &started, Path: projectDir + "/worktrees/" + childID.String()},
		}},
		nil,
		WithProjectDirs(projectDir, otherProjectDir),
		WithProjectSnapshotSource(fakeProjectSnapshotSource{
			snapshots: []ProjectInventorySnapshot{
				{
					ProjectID:   projectID,
					ProjectPath: projectDir,
					Tasks: []domain.Task{
						{
							ID:     rootID,
							Title:  "Correct project root",
							Status: domain.StatusInProgress,
							Type:   domain.TypeEpic,
						},
						{
							ID:       childID,
							Title:    "Correct project worker",
							Status:   domain.StatusInProgress,
							ParentID: &rootID,
							Session: &domain.Session{
								IssueID:   childID,
								State:     domain.SessionBusy,
								StartedAt: &started,
							},
							HasTmuxSession: true,
						},
					},
				},
				{
					ProjectID:   otherProjectID,
					ProjectPath: otherProjectDir,
					Tasks: []domain.Task{
						{
							ID:     rootID,
							Title:  "Wrong project root",
							Status: domain.StatusInReview,
							Type:   domain.TypeEpic,
						},
					},
				},
			},
		}),
	)

	snapshot, err := loader.ListTasksSnapshot(context.Background())
	if err != nil {
		t.Fatalf("ListTasksSnapshot: %v", err)
	}
	if len(snapshot.TreeTasks) != 1 {
		t.Fatalf("tree tasks = %#v, want one scoped root ancestor task", snapshot.TreeTasks)
	}
	if got := snapshot.TreeTasks[0]; got.ID != rootID || got.Title != "Correct project root" {
		t.Fatalf("tree ancestor = %#v, want correct project root", got)
	}
}

func TestGlobalInventoryLoaderHonorsLimit(t *testing.T) {
	loader := NewGlobalInventoryLoader(
		fakeSessionInventory{infos: []tmux.SessionInfo{
			{Name: "az-one"},
			{Name: "az-two"},
		}},
		nil,
		WithInventoryLimit(1),
	)
	snapshot, err := loader.ListTasksSnapshot(context.Background())
	if err != nil {
		t.Fatalf("ListTasksSnapshot: %v", err)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].SessionID != "az-one" {
		t.Fatalf("limited entries = %#v", snapshot.Entries)
	}
}

func TestGlobalInventoryLoaderSortsByLastAttachedDescending(t *testing.T) {
	oldest := time.Unix(1775200000, 0).UTC()
	middle := oldest.Add(30 * time.Minute)
	youngest := oldest.Add(90 * time.Minute)
	olderAttach := oldest.Add(2 * time.Hour)
	newerAttach := oldest.Add(3 * time.Hour)
	loader := NewGlobalInventoryLoader(
		fakeSessionInventory{infos: []tmux.SessionInfo{
			{Name: "az-youngest", CreatedAt: &youngest, LastAttachedAt: &olderAttach},
			{Name: "plain-unknown"},
			{Name: "az-oldest", CreatedAt: &oldest},
			{Name: "az-middle", CreatedAt: &middle, LastAttachedAt: &newerAttach},
		}},
		nil,
	)

	snapshot, err := loader.ListLiveSnapshot(context.Background())
	if err != nil {
		t.Fatalf("ListLiveSnapshot: %v", err)
	}
	got := make([]string, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		got = append(got, entry.SessionID)
	}
	want := []string{"az-middle", "az-youngest", "az-oldest", "plain-unknown"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("session order = %v, want %v", got, want)
	}
}

func TestGlobalInventoryLoaderSortsCurrentlyAttachedBeforeLastAttached(t *testing.T) {
	old := time.Unix(1775200000, 0).UTC()
	recent := old.Add(4 * time.Hour)
	loader := NewGlobalInventoryLoader(
		fakeSessionInventory{infos: []tmux.SessionInfo{
			{Name: "az-recent", LastAttachedAt: &recent},
			{Name: "az-attached", LastAttachedAt: &old, AttachedCount: 1},
			{Name: "az-multi", LastAttachedAt: &old, AttachedCount: 2},
		}},
		nil,
	)

	snapshot, err := loader.ListLiveSnapshot(context.Background())
	if err != nil {
		t.Fatalf("ListLiveSnapshot: %v", err)
	}
	got := make([]string, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		got = append(got, entry.SessionID)
	}
	want := []string{"az-multi", "az-attached", "az-recent"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("session order = %v, want %v", got, want)
	}
	if !snapshot.Entries[0].TmuxAttached || snapshot.Entries[0].TmuxAttachedCount != 2 {
		t.Fatalf("attached metadata = %#v, want attached count 2", snapshot.Entries[0])
	}
}

func TestGlobalInventoryLoaderUsesSessionPrefixBeforeGitRootResolution(t *testing.T) {
	root := t.TempDir()
	azRoot := root + "/azedarach"
	chRoot := root + "/Chefy"
	loader := NewGlobalInventoryLoader(
		fakeSessionInventory{},
		nil,
		WithProjectDirs(azRoot, chRoot),
	)

	dirs := loader.projectDirsForLiveSessions([]tmux.SessionInfo{
		{Name: "az-byh", Path: root + "/azedarach-byh"},
		{Name: "ch-wb", Path: root + "/Chefy-wb"},
	})

	want := []string{chRoot, azRoot}
	if strings.Join(dirs, "\n") != strings.Join(want, "\n") {
		t.Fatalf("project dirs = %#v, want configured roots %#v", dirs, want)
	}
}

func TestGlobalInventoryLoaderBindsLiveEntryToConfiguredProjectRootByPrefix(t *testing.T) {
	root := t.TempDir()
	azRoot := root + "/azedarach"
	chRoot := root + "/Chefy"
	loader := NewGlobalInventoryLoader(
		fakeSessionInventory{infos: []tmux.SessionInfo{
			{Name: "az-cfp", Path: root + "/azedarach-cfp"},
			{Name: "ch-we", Path: root + "/Chefy-we"},
		}},
		nil,
		WithProjectDirs(azRoot, chRoot),
	)

	snapshot, err := loader.ListLiveSnapshot(context.Background())
	if err != nil {
		t.Fatalf("ListLiveSnapshot: %v", err)
	}
	if len(snapshot.Entries) != 2 {
		t.Fatalf("entries = %#v, want 2", snapshot.Entries)
	}
	bySession := map[string]InventoryEntry{}
	for _, entry := range snapshot.Entries {
		bySession[entry.SessionID] = entry
	}
	if got := bySession["az-cfp"].ProjectPath; got != azRoot {
		t.Fatalf("az-cfp project path = %q, want %q", got, azRoot)
	}
	if got := bySession["ch-we"].ProjectPath; got != chRoot {
		t.Fatalf("ch-we project path = %q, want %q", got, chRoot)
	}
}

func TestGlobalInventoryLoaderDefersProjectDirProviderUntilEnrichment(t *testing.T) {
	root := t.TempDir()
	projectID := projectIDForPath(root)
	sessionID := naming.CanonicalSessionID(projectID, "lazy")
	providerCalls := 0
	loader := NewGlobalInventoryLoader(
		fakeSessionInventory{infos: []tmux.SessionInfo{
			{Name: sessionID, Path: root + "/worktrees/lazy"},
		}},
		nil,
		WithProjectDirsProvider(func() []string {
			providerCalls++
			return []string{root}
		}),
	)

	live, err := loader.ListLiveSnapshot(context.Background())
	if err != nil {
		t.Fatalf("ListLiveSnapshot: %v", err)
	}
	if providerCalls != 0 {
		t.Fatalf("project dir provider calls after live snapshot = %d, want 0", providerCalls)
	}
	if got := live.Entries[0].ProjectPath; got != "" {
		t.Fatalf("live project path = %q, want empty before enrichment", got)
	}

	enriched, err := loader.EnrichSnapshot(context.Background(), live)
	if err != nil {
		t.Fatalf("EnrichSnapshot: %v", err)
	}
	if providerCalls != 1 {
		t.Fatalf("project dir provider calls after enrichment = %d, want 1", providerCalls)
	}
	if got := enriched.Entries[0].ProjectPath; got != root {
		t.Fatalf("enriched project path = %q, want %q", got, root)
	}
	if enriched.Enriching {
		t.Fatalf("enriched snapshot still marked enriching")
	}
}

func TestGlobalInventoryLoaderRefreshesProjectDirProviderOnEachEnrichment(t *testing.T) {
	base := t.TempDir()
	firstRoot := filepath.Join(base, "azedarach")
	secondRoot := filepath.Join(base, "chefy")
	if err := os.MkdirAll(firstRoot, 0o755); err != nil {
		t.Fatalf("mkdir first root: %v", err)
	}
	if err := os.MkdirAll(secondRoot, 0o755); err != nil {
		t.Fatalf("mkdir second root: %v", err)
	}
	firstProjectID := naming.ProjectSessionPrefix(firstRoot)
	secondProjectID := naming.ProjectSessionPrefix(secondRoot)
	firstSessionID := naming.CanonicalSessionID(firstProjectID, "first")
	secondSessionID := naming.CanonicalSessionID(secondProjectID, "second")
	roots := []string{firstRoot}
	providerCalls := 0
	loader := NewGlobalInventoryLoader(
		fakeSessionInventory{},
		nil,
		WithProjectDirsProvider(func() []string {
			providerCalls++
			return append([]string(nil), roots...)
		}),
	)

	first := Snapshot{Entries: []InventoryEntry{{
		SessionID: firstSessionID,
		ProjectID: firstProjectID,
		IssueID:   "first",
		Worktree:  firstRoot + "/worktrees/first",
	}}}
	enriched, err := loader.EnrichSnapshot(context.Background(), first)
	if err != nil {
		t.Fatalf("first EnrichSnapshot: %v", err)
	}
	if providerCalls != 1 {
		t.Fatalf("provider calls after first enrichment = %d, want 1", providerCalls)
	}
	if got := enriched.Entries[0].ProjectPath; got != firstRoot {
		t.Fatalf("first enriched project path = %q, want %q", got, firstRoot)
	}

	roots = append(roots, secondRoot)
	second := Snapshot{Entries: []InventoryEntry{{
		SessionID: secondSessionID,
		ProjectID: secondProjectID,
		IssueID:   "second",
		Worktree:  secondRoot + "/worktrees/second",
	}}}
	enriched, err = loader.EnrichSnapshot(context.Background(), second)
	if err != nil {
		t.Fatalf("second EnrichSnapshot: %v", err)
	}
	if providerCalls != 2 {
		t.Fatalf("provider calls after second enrichment = %d, want 2", providerCalls)
	}
	if got := enriched.Entries[0].ProjectPath; got != secondRoot {
		t.Fatalf("second enriched project path = %q, want newly added %q", got, secondRoot)
	}
}

func TestTaskIDsByProjectDirTargetsOnlyLiveSessionIssues(t *testing.T) {
	root := t.TempDir()
	projectID := naming.ProjectSessionPrefix(root)
	entries := []InventoryEntry{
		{
			SessionID: naming.CanonicalSessionID(projectID, "az-1"),
			IssueID:   "az-1",
			ProjectID: projectID,
			Worktree:  filepath.Join(root, "worktrees", "az-1"),
		},
		{
			SessionID: naming.CanonicalSessionID(projectID, "az-2"),
			ProjectID: projectID,
			Worktree:  filepath.Join(root, "worktrees", "az-2"),
		},
		{
			SessionID: "plain-tmux",
			Worktree:  filepath.Join(root, "scratch"),
		},
	}

	got := taskIDsByProjectDir(entries, []string{root})
	key := cleanProjectDirKey(root)
	if strings.Join(got[key], ",") != "az-1,az-2" {
		t.Fatalf("targeted task ids = %#v, want az-1 and az-2 only", got)
	}
}

func TestTaskIDsByProjectDirDeduplicatesByProject(t *testing.T) {
	root := t.TempDir()
	projectID := naming.ProjectSessionPrefix(root)
	entries := []InventoryEntry{
		{SessionID: naming.CanonicalSessionID(projectID, "az-1"), ProjectID: projectID},
		{SessionID: naming.CanonicalSessionID(projectID, "az-1"), ProjectID: projectID},
	}

	got := taskIDsByProjectDir(entries, []string{root})
	key := cleanProjectDirKey(root)
	if len(got[key]) != 1 || got[key][0] != "az-1" {
		t.Fatalf("deduplicated task ids = %#v, want one az-1", got)
	}
}

func TestDaemonSnapshotSourceTargetedLoadRequestsAncestorContext(t *testing.T) {
	rootID := naming.IssueID("az-root")
	parentID := naming.IssueID("az-parent")
	childID := naming.IssueID("az-child")
	reader := &fakeTaskSnapshotReader{
		tasksByID: map[string]domain.Task{
			rootID.String(): {
				ID:    rootID,
				Title: "Root",
			},
			parentID.String(): {
				ID:       parentID,
				Title:    "Parent",
				ParentID: &rootID,
			},
			childID.String(): {
				ID:       childID,
				Title:    "Child",
				ParentID: &parentID,
			},
		},
	}
	source := NewDaemonSnapshotSourceForTasks(nil, nil, nil)

	snapshot, err := source.loadTaskSnapshot(context.Background(), reader, []string{childID.String()})
	if err != nil {
		t.Fatalf("loadTaskSnapshot: %v", err)
	}
	if reader.listCalls != 0 {
		t.Fatalf("ListTasksSnapshot calls = %d, want 0 for targeted load", reader.listCalls)
	}
	gotCalls := make([]string, 0, len(reader.metadataCalls))
	for _, call := range reader.metadataCalls {
		gotCalls = append(gotCalls, strings.Join(call, ","))
	}
	if strings.Join(gotCalls, "|") != "az-child" {
		t.Fatalf("GetManyTaskSnapshotWithAncestorsNoDependentsMetadataOnly calls = %v, want child only", gotCalls)
	}
	if len(reader.leanCalls) != 1 {
		t.Fatalf("underlying lean calls = %v, want one metadata-only delegated call", reader.leanCalls)
	}
	if len(reader.ancestorCalls) != 0 {
		t.Fatalf("GetManyTaskSnapshotWithAncestors calls = %v, want none", reader.ancestorCalls)
	}
	gotTasks := make(map[string]struct{}, len(snapshot.Tasks))
	for _, task := range snapshot.Tasks {
		gotTasks[task.ID.String()] = struct{}{}
	}
	for _, id := range []string{childID.String(), parentID.String(), rootID.String()} {
		if _, ok := gotTasks[id]; !ok {
			t.Fatalf("snapshot tasks missing %s: %#v", id, snapshot.Tasks)
		}
	}
}

func TestDaemonSnapshotSourceFallsBackToFullSnapshotWithoutTargetIDs(t *testing.T) {
	reader := &fakeTaskSnapshotReader{
		tasksByID: map[string]domain.Task{
			"az-1": {ID: "az-1", Title: "One"},
		},
	}
	source := NewDaemonSnapshotSourceForTasks(nil, nil, nil)

	snapshot, err := source.loadTaskSnapshot(context.Background(), reader, nil)
	if err != nil {
		t.Fatalf("loadTaskSnapshot: %v", err)
	}
	if reader.listCalls != 1 || len(reader.ancestorCalls) != 0 || len(reader.leanCalls) != 0 {
		t.Fatalf("reader calls = list:%d ancestors:%v lean:%v, want one list only", reader.listCalls, reader.ancestorCalls, reader.leanCalls)
	}
	if len(snapshot.Tasks) != 1 || snapshot.Tasks[0].ID.String() != "az-1" {
		t.Fatalf("snapshot tasks = %#v, want az-1", snapshot.Tasks)
	}
}

func TestGlobalInventoryLoaderDoesNotDiscoverProjectsFromUnmatchedSessions(t *testing.T) {
	root := t.TempDir()
	loader := NewGlobalInventoryLoader(
		fakeSessionInventory{},
		nil,
		WithProjectDirs(root+"/azedarach"),
	)

	dirs := loader.projectDirsForLiveSessions([]tmux.SessionInfo{
		{Name: "plain-tmux", Path: root + "/plain-repo"},
		{Name: "xy-unknown", Path: root + "/unknown-worktree"},
	})

	if len(dirs) != 0 {
		t.Fatalf("project dirs = %#v, want none for unmatched/plain sessions", dirs)
	}
}

func TestGlobalInventoryLoaderIncludesCurrentTmuxSession(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-123/default,1,0")
	loader := NewGlobalInventoryLoader(
		fakeSessionInventory{
			infos: []tmux.SessionInfo{
				{Name: "az-one"},
				{Name: "az-two"},
			},
			current: "az-two",
		},
		nil,
	)
	snapshot, err := loader.ListLiveSnapshot(context.Background())
	if err != nil {
		t.Fatalf("ListLiveSnapshot: %v", err)
	}
	if snapshot.CurrentSessionID != "az-two" {
		t.Fatalf("current session = %q, want az-two", snapshot.CurrentSessionID)
	}
}

func TestGlobalInventoryLoaderUsesExplicitCurrentSessionEnv(t *testing.T) {
	t.Setenv(currentSessionEnvKey, "az-env")
	loader := NewGlobalInventoryLoader(
		fakeSessionInventory{
			infos: []tmux.SessionInfo{
				{Name: "az-one"},
				{Name: "az-env"},
			},
			current: "az-one",
		},
		nil,
	)
	snapshot, err := loader.ListLiveSnapshot(context.Background())
	if err != nil {
		t.Fatalf("ListLiveSnapshot: %v", err)
	}
	if snapshot.CurrentSessionID != "az-env" {
		t.Fatalf("current session = %q, want env override az-env", snapshot.CurrentSessionID)
	}
}
