package tmuxselector

import (
	"context"
	"strings"
	"testing"
	"time"

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

func TestGlobalInventoryLoaderSortsBySessionStartOldestFirst(t *testing.T) {
	oldest := time.Unix(1775200000, 0).UTC()
	middle := oldest.Add(30 * time.Minute)
	youngest := oldest.Add(90 * time.Minute)
	loader := NewGlobalInventoryLoader(
		fakeSessionInventory{infos: []tmux.SessionInfo{
			{Name: "az-youngest", CreatedAt: &youngest},
			{Name: "plain-unknown"},
			{Name: "az-oldest", CreatedAt: &oldest},
			{Name: "az-middle", CreatedAt: &middle},
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
	want := []string{"az-oldest", "az-middle", "az-youngest", "plain-unknown"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("session order = %v, want %v", got, want)
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
