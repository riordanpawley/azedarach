package tmuxselector

import (
	"context"
	"testing"
	"time"

	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

type fakeSessionInventory struct {
	infos []tmux.SessionInfo
	err   error
}

func (f fakeSessionInventory) ListSessionInfos(context.Context) ([]tmux.SessionInfo, error) {
	return f.infos, f.err
}

type fakeProjectionStore struct {
	projectIDs []string
	sessions   map[string][]daemonstate.Session
	worktrees  map[string][]daemonstate.WorktreeState
}

func (f fakeProjectionStore) ListProjectIDs(context.Context) ([]string, error) {
	return append([]string(nil), f.projectIDs...), nil
}

func (f fakeProjectionStore) ListSessionStates(_ context.Context, projectID string) ([]daemonstate.Session, error) {
	return append([]daemonstate.Session(nil), f.sessions[projectID]...), nil
}

func (f fakeProjectionStore) ListWorktreeStates(_ context.Context, projectID string) ([]daemonstate.WorktreeState, error) {
	return append([]daemonstate.WorktreeState(nil), f.worktrees[projectID]...), nil
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
		WithRuntimeProjectionStores(fakeProjectionStore{
			projectIDs: []string{projectID},
			sessions: map[string][]daemonstate.Session{
				projectID: {
					{
						ID:            sessionID,
						IssueID:       "bxo",
						ObservedState: daemonstate.SessionStatePaused,
						StartedAt:     &started,
					},
				},
			},
			worktrees: map[string][]daemonstate.WorktreeState{
				projectID: {{ProjectID: projectID, IssueID: "bxo", Path: worktree}},
			},
		}),
	)

	snapshot, err := loader.ListTasksSnapshot(context.Background())
	if err != nil {
		t.Fatalf("ListTasksSnapshot: %v", err)
	}
	if len(snapshot.Entries) != 2 {
		t.Fatalf("entries = %#v, want 2 Az sessions", snapshot.Entries)
	}
	first := snapshot.Entries[0]
	if first.SessionID != sessionID || first.IssueID != "bxo" || first.ProjectID != projectID {
		t.Fatalf("first entry = %#v", first)
	}
	if first.Worktree != worktree || !first.HasWorktree || first.State != domain.SessionPaused {
		t.Fatalf("first runtime metadata = %#v", first)
	}
	if snapshot.Entries[1].SessionID != "az-bxk" || snapshot.Entries[1].IssueID != "bxk" {
		t.Fatalf("az-prefixed fallback entry = %#v", snapshot.Entries[1])
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
