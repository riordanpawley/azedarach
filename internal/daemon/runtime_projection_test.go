package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

func TestBuildRuntimeProjectionPopulatesSessionAndWorktreeSignals(t *testing.T) {
	sessionUpdatedAt := time.Date(2026, time.April, 1, 11, 0, 0, 0, time.UTC)
	worktreeUpdatedAt := time.Date(2026, time.April, 1, 11, 5, 0, 0, time.UTC)
	gitStatusRaw, err := json.Marshal(git.GitStatus{
		Modified:   []string{"README.md"},
		HasChanges: true,
	})
	if err != nil {
		t.Fatalf("marshal git status: %v", err)
	}

	projection := buildRuntimeProjection("proj-runtime", &daemonstate.Session{
		ID:        "sess-7",
		IssueID:   "az-7",
		State:     daemonstate.SessionStateAttached,
		UpdatedAt: sessionUpdatedAt,
	}, &daemonstate.WorktreeState{
		ProjectID:        "proj-runtime",
		IssueID:          "az-7",
		Path:             "/tmp/repo-az-7",
		Branch:           "riordan/az-7/task",
		UpdatedAt:        worktreeUpdatedAt,
		GitStatusRaw:     gitStatusRaw,
		GitStatusUpdated: &worktreeUpdatedAt,
	})

	if projection.ProjectID != "proj-runtime" {
		t.Fatalf("project id = %q, want proj-runtime", projection.ProjectID)
	}
	if projection.IssueID != "az-7" {
		t.Fatalf("issue id = %q, want az-7", projection.IssueID)
	}
	if !projection.Session.HasSession || projection.Session.SessionID != "sess-7" {
		t.Fatalf("session = %+v, want active sess-7", projection.Session)
	}
	if projection.Session.State != "attached" {
		t.Fatalf("session state = %q, want attached", projection.Session.State)
	}
	if projection.Session.Worktree != "/tmp/repo-az-7" {
		t.Fatalf("session worktree = %q, want /tmp/repo-az-7", projection.Session.Worktree)
	}
	if projection.Agent.Status != "attached" || projection.Agent.SessionID != "sess-7" {
		t.Fatalf("agent = %+v, want attached sess-7", projection.Agent)
	}
	if !projection.Worktree.Exists || !projection.Worktree.Healthy {
		t.Fatalf("worktree = %+v, want exists+healthy", projection.Worktree)
	}
	if projection.Worktree.Path != "/tmp/repo-az-7" || projection.Worktree.Branch != "riordan/az-7/task" {
		t.Fatalf("worktree = %+v, want populated path/branch", projection.Worktree)
	}
	if !projection.Worktree.GitStatusUpdatedAt.Equal(worktreeUpdatedAt) {
		t.Fatalf("worktree git status updated_at = %v, want %v", projection.Worktree.GitStatusUpdatedAt, worktreeUpdatedAt)
	}
	if !projection.Git.HasUncommittedChanges {
		t.Fatalf("git projection = %+v, want dirty status", projection.Git)
	}
	if projection.Git.GitAheadCount != 0 || projection.Git.GitBehindCount != 0 || projection.Git.GitAdditions != 0 || projection.Git.GitDeletions != 0 {
		t.Fatalf("git counters = %+v, want zero-value counters until full delta wiring lands", projection.Git)
	}
}

func TestBuildRuntimeProjectionUsesObservedSessionStateWhenPresent(t *testing.T) {
	updatedAt := time.Date(2026, time.April, 1, 11, 0, 0, 0, time.UTC)
	projection := buildRuntimeProjection("proj-runtime", &daemonstate.Session{
		ID:            "sess-7",
		IssueID:       "az-7",
		State:         daemonstate.SessionStateStarting,
		ObservedState: daemonstate.SessionStateAttached,
		UpdatedAt:     updatedAt,
	}, nil)

	if projection.Session.State != "attached" {
		t.Fatalf("session state = %q, want attached observed state", projection.Session.State)
	}
	if projection.Agent.Status != "attached" {
		t.Fatalf("agent status = %q, want attached observed state", projection.Agent.Status)
	}
}

func TestBuildRuntimeProjectionZeroValueBehavior(t *testing.T) {
	projection := buildRuntimeProjection("", nil, nil)

	if projection.ProjectID != "default" {
		t.Fatalf("project id = %q, want default", projection.ProjectID)
	}
	if projection.IssueID != "" {
		t.Fatalf("issue id = %q, want empty", projection.IssueID)
	}
	if projection.Session.HasSession || projection.Session.SessionID != "" || projection.Session.State != "" {
		t.Fatalf("session = %+v, want zero value", projection.Session)
	}
	if projection.Agent.Status != "" || projection.Agent.SessionID != "" || projection.Agent.UpdatedAt != nil {
		t.Fatalf("agent = %+v, want zero value", projection.Agent)
	}
	if projection.Worktree.Exists || projection.Worktree.Path != "" || projection.Worktree.Branch != "" || projection.Worktree.Healthy {
		t.Fatalf("worktree = %+v, want zero value", projection.Worktree)
	}
	if projection.Git.HasUncommittedChanges || projection.Git.GitAheadCount != 0 || projection.Git.GitBehindCount != 0 {
		t.Fatalf("git = %+v, want zero value", projection.Git)
	}
}

func TestBuildRuntimeProjectionSnapshotAndEventBodyAreDeterministic(t *testing.T) {
	projection := buildRuntimeProjection("proj-x", &daemonstate.Session{
		ID:        "sess-x",
		IssueID:   "az-x",
		State:     daemonstate.SessionStateStarting,
		UpdatedAt: time.Date(2026, time.April, 1, 12, 0, 0, 0, time.UTC),
	}, &daemonstate.WorktreeState{
		ProjectID: "proj-x",
		IssueID:   "az-x",
		Path:      "/tmp/repo-az-x",
		Branch:    "riordan/az-x/task",
		UpdatedAt: time.Date(2026, time.April, 1, 12, 1, 0, 0, time.UTC),
	})

	snapshot := buildRuntimeProjectionSnapshot("proj-x", 17, []protocol.RuntimeProjection{projection})
	if snapshot.ProjectID != "proj-x" || snapshot.SnapshotRevision != 17 {
		t.Fatalf("snapshot header = %+v, want project proj-x revision 17", snapshot)
	}
	if len(snapshot.Projections) != 1 || snapshot.Projections[0].IssueID != "az-x" {
		t.Fatalf("snapshot projections = %+v, want one az-x record", snapshot.Projections)
	}

	body := buildRuntimeProjectionEventBody("proj-x", 18, projection)
	if body.ProjectID != "proj-x" || body.Revision != 18 || body.Projection.IssueID != "az-x" {
		t.Fatalf("event body = %+v, want proj-x revision 18 az-x", body)
	}
}

func TestPublishSessionProjectionEventIncludesRuntimeDelta(t *testing.T) {
	ctx := context.Background()
	sessionStore := daemonstate.NewStore()
	runtimeStateStore := newRuntimeProjectionStore(t)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	const (
		projectID = "proj-runtime"
		issueID   = "az-1"
		sessionID = "sess-1"
		worktree  = "/tmp/repo-az-1"
		branch    = "riordan/az-1/task"
	)
	sessionUpdatedAt := time.Date(2026, time.April, 1, 13, 0, 0, 0, time.UTC)
	worktreeUpdatedAt := time.Date(2026, time.April, 1, 13, 5, 0, 0, time.UTC)
	if err := runtimeStateStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStateAttached,
		UpdatedAt: sessionUpdatedAt,
	}); err != nil {
		t.Fatalf("seed session projection: %v", err)
	}
	if err := runtimeStateStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   issueID,
		Path:      worktree,
		Branch:    branch,
		UpdatedAt: worktreeUpdatedAt,
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	daemon := &Daemon{
		cfg: Config{RepoDir: ".", BaseBranch: "main", Logger: slog.Default()},
		hub: publish.NewHub(32, 16, slog.Default()),
		git: git.NewClient(&recordingGitRunner{runFn: func(args ...string) (string, error) {
			switch {
			case len(args) >= 4 && args[0] == "-C" && args[1] == worktree && args[2] == "merge-base":
				return "merge-base-sha\n", nil
			case len(args) >= 7 && args[0] == "-C" && args[1] == worktree && args[2] == "diff" && args[3] == "--shortstat":
				return " 1 file changed, 2 insertions(+), 1 deletion(-)\n", nil
			default:
				return "", nil
			}
		}}, slog.Default()),
		sessionStore: sessionStore,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	ch, cancel := daemon.hub.Subscribe(projectID, 0)
	defer cancel()

	rev := daemon.publishSessionProjectionEvent(ctx, projectID, protocol.Metadata{ProjectID: projectID}, daemonstate.Session{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStateAttached,
		UpdatedAt: sessionUpdatedAt,
	})
	if rev != 1 {
		t.Fatalf("revision = %d, want 1", rev)
	}

	evt := waitForRuntimeProjectionEvent(t, ch)
	if evt.Event != protocol.EventSessionUpdated {
		t.Fatalf("event = %s, want %s", evt.Event, protocol.EventSessionUpdated)
	}
	var body protocol.SessionProjectionEventBody
	if err := json.Unmarshal(evt.Body, &body); err != nil {
		t.Fatalf("unmarshal session projection event: %v", err)
	}
	if body.Runtime == nil {
		t.Fatal("expected runtime projection delta")
	}
	if body.Runtime.ProjectID != projectID || body.Runtime.Revision != evt.Revision {
		t.Fatalf("runtime envelope = %+v, want project/revision %s/%d", body.Runtime, projectID, evt.Revision)
	}
	if body.Runtime.Projection.IssueID != issueID {
		t.Fatalf("runtime issue = %s, want %s", body.Runtime.Projection.IssueID, issueID)
	}
	if body.Runtime.Projection.Session.SessionID != sessionID || body.Runtime.Projection.Session.State != protocol.SessionLifecycleStateAttached {
		t.Fatalf("runtime session = %+v, want attached session %s", body.Runtime.Projection.Session, sessionID)
	}
	if body.Runtime.Projection.Worktree.Path != worktree || body.Runtime.Projection.Worktree.Branch != branch {
		t.Fatalf("runtime worktree = %+v, want %s/%s", body.Runtime.Projection.Worktree, worktree, branch)
	}
}

func TestPublishGitStatusProjectionEventIncludesRuntimeDelta(t *testing.T) {
	ctx := context.Background()
	sessionStore := daemonstate.NewStore()
	runtimeStateStore := newRuntimeProjectionStore(t)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	const (
		projectID = "proj-runtime"
		issueID   = "az-2"
		sessionID = "sess-2"
		worktree  = "/tmp/repo-az-2"
		branch    = "riordan/az-2/task"
	)
	if err := runtimeStateStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStateStarting,
		UpdatedAt: time.Date(2026, time.April, 1, 14, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed session projection: %v", err)
	}
	if err := runtimeStateStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   issueID,
		Path:      worktree,
		Branch:    branch,
		UpdatedAt: time.Date(2026, time.April, 1, 14, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}
	rawStatus, err := json.Marshal(git.GitStatus{
		Modified:   []string{"changed.go"},
		HasChanges: true,
	})
	if err != nil {
		t.Fatalf("marshal git status: %v", err)
	}
	if err := runtimeStateStore.UpsertWorktreeStateGitStatus(ctx, projectID, issueID, rawStatus, time.Date(2026, time.April, 1, 14, 5, 0, 0, time.UTC)); err != nil {
		t.Fatalf("seed git status projection: %v", err)
	}

	daemon := &Daemon{
		cfg: Config{RepoDir: ".", BaseBranch: "main", Logger: slog.Default()},
		hub: publish.NewHub(32, 16, slog.Default()),
		git: git.NewClient(&recordingGitRunner{runFn: func(args ...string) (string, error) {
			switch {
			case len(args) >= 4 && args[0] == "-C" && args[1] == worktree && args[2] == "merge-base":
				return "merge-base-sha\n", nil
			case len(args) >= 7 && args[0] == "-C" && args[1] == worktree && args[2] == "diff" && args[3] == "--shortstat":
				return " 1 file changed, 2 insertions(+), 1 deletion(-)\n", nil
			default:
				return "", nil
			}
		}}, slog.Default()),
		sessionStore: sessionStore,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	ch, cancel := daemon.hub.Subscribe(projectID, 0)
	defer cancel()

	rev := daemon.publishGitStatusProjectionEvent(ctx, projectID, issueID, worktree, &git.GitStatus{
		Modified:   []string{"changed.go"},
		HasChanges: true,
	})
	if rev != 1 {
		t.Fatalf("revision = %d, want 1", rev)
	}

	evt := waitForRuntimeProjectionEvent(t, ch)
	if evt.Event != protocol.EventGitStatusUpdated {
		t.Fatalf("event = %s, want %s", evt.Event, protocol.EventGitStatusUpdated)
	}
	var body protocol.ProjectionUpdateEventBody
	if err := json.Unmarshal(evt.Body, &body); err != nil {
		t.Fatalf("unmarshal git status event: %v", err)
	}
	if body.Runtime == nil {
		t.Fatal("expected runtime projection delta")
	}
	if body.Runtime.ProjectID != projectID || body.Runtime.Revision != evt.Revision {
		t.Fatalf("runtime envelope = %+v, want project/revision %s/%d", body.Runtime, projectID, evt.Revision)
	}
	if body.Runtime.Projection.Git.HasUncommittedChanges != true {
		t.Fatalf("runtime git dirty = %v, want true", body.Runtime.Projection.Git.HasUncommittedChanges)
	}
	if body.Runtime.Projection.Git.GitAdditions != 0 || body.Runtime.Projection.Git.GitDeletions != 0 {
		t.Fatalf("runtime git stats = %+v, want additions/deletions 0/0 when line totals are unavailable", body.Runtime.Projection.Git)
	}
	if body.Runtime.Projection.Session.SessionID != sessionID || body.Runtime.Projection.Worktree.Path != worktree {
		t.Fatalf("runtime projection = %+v, want session/worktree %s/%s", body.Runtime.Projection, sessionID, worktree)
	}
}

func TestPublishWorktreeProjectionEventIncludesRuntimeDelta(t *testing.T) {
	ctx := context.Background()
	sessionStore := daemonstate.NewStore()
	runtimeStateStore := newRuntimeProjectionStore(t)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	const (
		projectID = "proj-runtime"
		issueID   = "az-3"
		sessionID = "sess-3"
		worktree  = "/tmp/repo-az-3"
		branch    = "riordan/az-3/task"
	)
	if err := runtimeStateStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStatePaused,
		UpdatedAt: time.Date(2026, time.April, 1, 15, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed session projection: %v", err)
	}
	if err := runtimeStateStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   issueID,
		Path:      worktree,
		Branch:    branch,
		UpdatedAt: time.Date(2026, time.April, 1, 15, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}
	rawStatus, err := json.Marshal(git.GitStatus{
		Added:          []string{"new.go"},
		Modified:       []string{"changed.go"},
		Staged:         []string{"staged.go"},
		Deleted:        []string{"removed.go"},
		HasChanges:     true,
		GitAdditions:   3,
		GitDeletions:   1,
		GitAheadCount:  2,
		GitBehindCount: 1,
	})
	if err != nil {
		t.Fatalf("marshal git status: %v", err)
	}
	if err := runtimeStateStore.UpsertWorktreeStateGitStatus(ctx, projectID, issueID, rawStatus, time.Date(2026, time.April, 1, 15, 5, 0, 0, time.UTC)); err != nil {
		t.Fatalf("seed git status projection: %v", err)
	}

	daemon := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		hub:          publish.NewHub(32, 16, slog.Default()),
		sessionStore: sessionStore,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	ch, cancel := daemon.hub.Subscribe(projectID, 0)
	defer cancel()

	rev := daemon.publishWorktreeProjectionEvent(ctx, projectID, issueID, worktree)
	if rev != 1 {
		t.Fatalf("revision = %d, want 1", rev)
	}

	evt := waitForRuntimeProjectionEvent(t, ch)
	if evt.Event != protocol.EventWorktreeProjectionUpdated {
		t.Fatalf("event = %s, want %s", evt.Event, protocol.EventWorktreeProjectionUpdated)
	}
	var body protocol.ProjectionUpdateEventBody
	if err := json.Unmarshal(evt.Body, &body); err != nil {
		t.Fatalf("unmarshal worktree event: %v", err)
	}
	if body.Runtime == nil {
		t.Fatal("expected runtime projection delta")
	}
	if body.Runtime.ProjectID != projectID || body.Runtime.Revision != evt.Revision {
		t.Fatalf("runtime envelope = %+v, want project/revision %s/%d", body.Runtime, projectID, evt.Revision)
	}
	if body.Runtime.Projection.IssueID != issueID {
		t.Fatalf("runtime issue = %s, want %s", body.Runtime.Projection.IssueID, issueID)
	}
	if body.Runtime.Projection.Worktree.Path != worktree || body.Runtime.Projection.Worktree.Branch != branch {
		t.Fatalf("runtime worktree = %+v, want %s/%s", body.Runtime.Projection.Worktree, worktree, branch)
	}
	if body.Runtime.Projection.Session.SessionID != sessionID {
		t.Fatalf("runtime session = %+v, want session %s", body.Runtime.Projection.Session, sessionID)
	}
	if body.Runtime.Projection.Git.GitAdditions != 3 || body.Runtime.Projection.Git.GitDeletions != 1 {
		t.Fatalf("runtime git stats = %+v, want additions/deletions 3/1", body.Runtime.Projection.Git)
	}
	if body.Runtime.Projection.Git.GitAheadCount != 2 || body.Runtime.Projection.Git.GitBehindCount != 1 {
		t.Fatalf("runtime git ahead/behind = %+v, want 2/1", body.Runtime.Projection.Git)
	}
}

func newRuntimeProjectionStore(t *testing.T) *daemonstate.RuntimeStateStore {
	t.Helper()
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	return store
}

func waitForRuntimeProjectionEvent(t *testing.T, ch <-chan protocol.EventEnvelope) protocol.EventEnvelope {
	t.Helper()
	select {
	case evt := <-ch:
		return evt
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runtime projection event")
		return protocol.EventEnvelope{}
	}
}
