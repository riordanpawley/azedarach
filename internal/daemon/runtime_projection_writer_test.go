package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/daemon/userstore"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

type recordingRuntimeProjectionWriter struct {
	mu                sync.Mutex
	calls             []string
	publishedSessions []daemonstate.Session
}

func (r *recordingRuntimeProjectionWriter) record(call string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
}

func (r *recordingRuntimeProjectionWriter) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func (r *recordingRuntimeProjectionWriter) sessionSnapshot() []daemonstate.Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]daemonstate.Session(nil), r.publishedSessions...)
}

func (r *recordingRuntimeProjectionWriter) PersistSessionProjection(context.Context, string, daemonstate.Session) error {
	r.record("session.persist")
	return nil
}

func (r *recordingRuntimeProjectionWriter) PersistSessionProjectionAndPublish(context.Context, string, protocol.Metadata, daemonstate.Session) (uint64, error) {
	r.record("session.persist+publish")
	return 1, nil
}

func (r *recordingRuntimeProjectionWriter) PublishSessionProjectionEvent(_ context.Context, _ string, _ protocol.Metadata, session daemonstate.Session) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "session.publish")
	r.publishedSessions = append(r.publishedSessions, session)
	return 2
}

func (r *recordingRuntimeProjectionWriter) ReplaceSessionProjectionSnapshot(context.Context, string, []daemonstate.Session) error {
	r.record("session.snapshot.replace")
	return nil
}

func (r *recordingRuntimeProjectionWriter) PersistWorktreeProjection(context.Context, string, string, string, string) error {
	r.record("worktree.persist")
	return nil
}

func (r *recordingRuntimeProjectionWriter) PersistWorktreeProjectionAndPublish(context.Context, string, string, string, string) (uint64, error) {
	r.record("worktree.persist+publish")
	return 3, nil
}

func (r *recordingRuntimeProjectionWriter) DeleteWorktreeProjectionAndPublish(context.Context, string, string) uint64 {
	r.record("worktree.delete+publish")
	return 4
}

func (r *recordingRuntimeProjectionWriter) PublishWorktreeProjectionEvent(context.Context, string, string, string) uint64 {
	r.record("worktree.publish")
	return 5
}

func (r *recordingRuntimeProjectionWriter) ReplaceWorktreeProjectionSnapshot(context.Context, string, []daemonstate.WorktreeState) error {
	r.record("worktree.snapshot.replace")
	return nil
}

func (r *recordingRuntimeProjectionWriter) PersistGitStatusProjectionAndPublish(context.Context, string, string, string, *git.GitStatus, bool, bool) (uint64, error) {
	r.record("git.persist+publish")
	return 6, nil
}

func (r *recordingRuntimeProjectionWriter) PersistGitHookStatusProjectionAndPublishResult(context.Context, string, string, string, int64, *git.GitStatus) (uint64, error) {
	r.record("git.hook.persist+publish")
	return 6, nil
}

func (r *recordingRuntimeProjectionWriter) PublishGitStatusProjectionEvent(context.Context, string, string, string, *git.GitStatus) uint64 {
	r.record("git.publish")
	return 7
}

type statusRunner struct {
	status string
}

func TestRuntimeProjectionWriterAttributesContendedHolderAndWaiter(t *testing.T) {
	d := &Daemon{cfg: Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	writer := newRuntimeProjectionWriter(d)
	holderCtx := contextWithRuntimeProjectionWriterOperation(context.Background(), "worktree.replace_snapshot")
	releaseHolder, err := writer.lockProjectionWriter(holderCtx, "proj-attribution", "fallback.holder")
	if err != nil {
		t.Fatal(err)
	}

	attributed := make(chan [2]string, 1)
	queued := make(chan struct{})
	waiterCtx := contextWithRuntimeProjectionWriterOperation(context.Background(), "command.runtime.signal.ingest")
	waiterCtx = withRuntimeProjectionWriterQueuedHookForTest(waiterCtx, func(string) { close(queued) })
	waiterCtx = withRuntimeProjectionWriterWaitHookForTest(waiterCtx, func(waiter, holder string) {
		attributed <- [2]string{waiter, holder}
	})
	waiterDone := make(chan struct{})
	go func() {
		releaseWaiter, err := writer.lockProjectionWriter(waiterCtx, "proj-attribution", "fallback.waiter")
		if err != nil {
			t.Errorf("waiter lock: %v", err)
			close(waiterDone)
			return
		}
		releaseWaiter()
		close(waiterDone)
	}()

	<-queued
	releaseHolder()
	got := <-attributed
	if got != [2]string{"command.runtime.signal.ingest", "worktree.replace_snapshot"} {
		t.Fatalf("writer attribution = %q/%q", got[0], got[1])
	}
	<-waiterDone
}

func TestRuntimeProjectionWriterCanceledAdmissionReturnsErrorAndDoesNotPersist(t *testing.T) {
	ctx := context.Background()
	store := newRuntimeProjectionStore(t)
	t.Cleanup(func() { _ = store.Close() })
	projectID := "proj-canceled-writer"
	d := &Daemon{
		cfg:                 Config{RepoDir: ".", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{".": store},
	}
	writer := newRuntimeProjectionWriter(d)
	release, err := writer.lockProjectionWriter(ctx, projectID, "holder")
	if err != nil {
		t.Fatal(err)
	}
	queued := make(chan struct{})
	waitCtx, cancel := context.WithCancel(withRuntimeProjectionWriterQueuedHookForTest(ctx, func(string) { close(queued) }))
	result := make(chan error, 1)
	go func() {
		_, persistErr := writer.PersistWorktreeProjectionAndPublish(waitCtx, projectID, "az-cancel", "/tmp/az-cancel", "az/cancel")
		result <- persistErr
	}()
	<-queued
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("persist error = %v, want context canceled", err)
	}
	release()
	if rows, err := store.ListWorktreeStates(ctx, projectID); err != nil || len(rows) != 0 {
		t.Fatalf("worktree rows after canceled admission = %+v err=%v", rows, err)
	}
}

func TestGitHookPublicationCheckpointPreventsRepublishAfterPostCommitCrash(t *testing.T) {
	ctx := context.Background()
	store := newRuntimeProjectionStore(t)
	t.Cleanup(func() { _ = store.Close() })
	projectID := "proj-hook-crash"
	issueID := "az-hook-crash"
	worktree := "/tmp/az-hook-crash"
	if err := store.UpsertWorktreeState(ctx, daemonstate.WorktreeState{ProjectID: projectID, IssueID: issueID, Path: worktree, Branch: "az/hook-crash", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	intent, err := store.AcceptGitHookRefresh(ctx, projectID, worktree, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		cfg:                 Config{RepoDir: ".", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		hub:                 publish.NewHub(8, 4, slog.Default()),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{".": store},
		revision:            map[string]uint64{},
	}
	writer := newRuntimeProjectionWriter(d)
	crashErr := errors.New("injected crash after durable publication commit")
	crashCtx := withGitHookPublicationCommittedHookForTest(ctx, func() error { return crashErr })
	if _, err := writer.PersistGitHookStatusProjectionAndPublishResult(crashCtx, projectID, issueID, worktree, intent.RequestedGeneration, cleanGitStatus()); !errors.Is(err, crashErr) {
		t.Fatalf("post-commit crash error = %v", err)
	}
	if pending, err := store.ListPendingGitHookRefreshes(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("pending after durable commit = %+v err=%v", pending, err)
	}
	if rev, err := writer.PersistGitHookStatusProjectionAndPublishResult(ctx, projectID, issueID, worktree, intent.RequestedGeneration, cleanGitStatus()); err != nil || rev != 0 {
		t.Fatalf("reopen replay result revision=%d err=%v, want idempotent no-op", rev, err)
	}
	if got := d.currentRevision(projectID); got != 1 {
		t.Fatalf("logical publication revisions = %d, want one durable publication allocation", got)
	}
}

func (r statusRunner) Run(_ context.Context, args ...string) (string, error) {
	if len(args) >= 4 && args[0] == "-C" && args[2] == "status" && args[3] == "--porcelain" {
		return r.status, nil
	}
	return "", fmt.Errorf("unexpected git command: %v", args)
}

func TestRuntimeProjectionHelpersRouteThroughSingleWriter(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessionStore := daemonstate.NewStore()
	runtimeStateStore := newRuntimeProjectionStore(t)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	const (
		projectID = "proj-writer"
		issueID   = "az-1"
		sessionID = "sess-1"
		worktree  = "/tmp/repo-az-1"
		branch    = "riordan/az-1/task"
	)
	if _, err := sessionStore.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateStarting); err != nil {
		t.Fatalf("seed session store: %v", err)
	}
	if err := runtimeStateStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   issueID,
		Path:      worktree,
		Branch:    branch,
		UpdatedAt: time.Date(2026, time.April, 2, 11, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed worktree state: %v", err)
	}

	writer := &recordingRuntimeProjectionWriter{}
	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: logger},
		sessionStore: sessionStore,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
		runtimeProjectionWriter: writer,
	}

	if err := d.upsertSessionAndPublish(projectID, sessionID, issueID, daemonstate.SessionStateStarting); err != nil {
		t.Fatalf("upsertSessionAndPublish: %v", err)
	}
	d.writeSessionStopProjection(projectID, sessionID, issueID)
	if err := d.persistTmuxSessionRuntimeState(ctx, projectID, []tmux.SessionInfo{{Name: sessionID}}, nil); err != nil {
		t.Fatalf("persistTmuxSessionRuntimeState: %v", err)
	}

	wa := &worktreeServiceAdapter{
		runtimeProjectionWriter: writer,
		runtimeStateStore:       runtimeStateStore,
		logger:                  logger,
	}
	wa.writeWorktreeProjectionSnapshot(ctx, projectID, []git.Worktree{{IssueID: issueID, Path: worktree, Branch: branch}}, nil)

	ga := &gitServiceAdapter{
		client:                  git.NewClient(statusRunner{status: " M README.md\n"}, logger),
		runtimeStateStore:       runtimeStateStore,
		runtimeProjectionWriter: writer,
		logger:                  logger,
	}
	ga.refreshGitStatusWriteThrough(ctx, projectID, worktree, true, false)

	got := strings.Join(writer.snapshot(), ",")
	for _, want := range []string{
		"session.persist+publish",
		"session.persist",
		"session.persist",
		"worktree.snapshot.replace",
		"git.persist+publish",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("writer calls %q missing %q", got, want)
		}
	}
}

func TestRuntimeProjectionWriterPersistsBeforePublishingSessionEvents(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessionStore := daemonstate.NewStore()
	runtimeStateStore := newRuntimeProjectionStore(t)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	const (
		projectID = "proj-session"
		issueID   = "az-2"
		sessionID = "sess-2"
	)
	session := daemonstate.Session{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStateAttached,
		UpdatedAt: time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC),
	}
	if _, err := sessionStore.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateStarting); err != nil {
		t.Fatalf("seed session store: %v", err)
	}

	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: logger},
		hub:          publish.NewHub(8, 4, logger),
		sessionStore: sessionStore,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}
	writer := newRuntimeProjectionWriter(d)

	ch, cancel := d.hub.Subscribe(projectID, 0)
	defer cancel()

	rev, err := writer.PersistSessionProjectionAndPublish(ctx, projectID, protocol.Metadata{ProjectID: projectID}, session)
	if err != nil {
		t.Fatal(err)
	}
	if rev != 1 {
		t.Fatalf("revision = %d, want 1", rev)
	}

	select {
	case evt := <-ch:
		if evt.Revision != 1 {
			t.Fatalf("event revision = %d, want 1", evt.Revision)
		}
		var body protocol.SessionProjectionEventBody
		if err := json.Unmarshal(evt.Body, &body); err != nil {
			t.Fatalf("unmarshal session projection event: %v", err)
		}
		if body.Session.SessionID != sessionID || body.Session.IssueID != issueID {
			t.Fatalf("event session = %+v, want %s/%s", body.Session, sessionID, issueID)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for session projection event")
	}

	sessions, err := runtimeStateStore.ListSessionStates(ctx, projectID)
	if err != nil {
		t.Fatalf("ListSessionStates: %v", err)
	}
	if got, want := len(sessions), 1; got != want {
		t.Fatalf("session row count = %d, want %d", got, want)
	}
	if sessions[0].ID != sessionID || sessions[0].IssueID != issueID {
		t.Fatalf("session row = %+v", sessions[0])
	}
}

func TestRuntimeProjectionWriterReleasesLockBeforeReadModelRefresh(t *testing.T) {
	ctx := context.Background()
	runtimeStateStore := newRuntimeProjectionStore(t)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	const (
		projectID = "proj-refresh-outside-writer-lock"
		issueID   = "az-refresh"
		sessionID = "sess-refresh"
	)
	canonical := domain.Task{ID: naming.IssueID(issueID), Title: "refresh overlap", Type: domain.TypeTask}
	refreshEntered := make(chan struct{})
	releaseRefresh := make(chan struct{})
	materializer := newProjectReadMaterializer(projectID, nil, func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) {
		close(refreshEntered)
		<-releaseRefresh
		return tasks, nil
	})
	canonicalByID := map[string]domain.Task{issueID: canonical}
	issueKeys, runtimeKeys := checkpointMaterializedTasks(canonicalByID, canonicalByID)
	materializer.replaceBootstrap(canonicalByID, canonicalByID, protocol.MaterializedSnapshotMetadata{Health: "healthy"}, issueKeys, runtimeKeys)

	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
		materializers: map[string]*projectReadMaterializer{projectID: materializer},
	}
	writer := newRuntimeProjectionWriter(d)
	done := make(chan error, 1)
	go func() {
		done <- writer.PersistSessionProjection(ctx, projectID, daemonstate.Session{
			ID: sessionID, IssueID: issueID, State: daemonstate.SessionStateRunning, UpdatedAt: time.Now().UTC(),
		})
	}()

	<-refreshEntered
	lockAvailable := writer.mu.currentHolder() == ""
	close(releaseRefresh)
	if err := <-done; err != nil {
		t.Fatalf("PersistSessionProjection: %v", err)
	}
	if !lockAvailable {
		t.Fatal("projection writer lock remained held across read-model refresh")
	}
	if session, found, err := runtimeStateStore.GetSessionState(ctx, projectID, sessionID); err != nil || !found || session.IssueID != issueID {
		t.Fatalf("persisted session = %+v found=%v err=%v", session, found, err)
	}
}

func TestRuntimeProjectionWriterSnapshotReplacementRefreshesRemovedIssueKeys(t *testing.T) {
	ctx := context.Background()
	const (
		projectID = "proj-snapshot-removal"
		issueID   = "az-removed"
	)
	now := time.Date(2026, time.April, 2, 12, 30, 0, 0, time.UTC)
	runtimeStore := newRuntimeProjectionStore(t)
	t.Cleanup(func() { _ = runtimeStore.Close() })
	if err := runtimeStore.ReplaceSessionStates(ctx, projectID, []daemonstate.Session{{
		ID: "sess-removed", IssueID: issueID, State: daemonstate.SessionStateRunning, UpdatedAt: now,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := runtimeStore.ReplaceWorktreeStates(ctx, projectID, []daemonstate.WorktreeState{{
		ProjectID: projectID, IssueID: issueID, Path: "/tmp/removed", Branch: "issue/removed", UpdatedAt: now,
	}}); err != nil {
		t.Fatal(err)
	}
	rootStore, err := userstore.Open(filepath.Join(t.TempDir(), "user.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rootStore.Close() })
	canonical := domain.Task{ID: naming.IssueID(issueID), Title: "removed runtime", Status: domain.StatusInProgress, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now}
	projected := canonical
	projected.Session = &domain.Session{IssueID: naming.IssueID(issueID), State: domain.SessionBusy, UpdatedAt: now}
	projected.HasTmuxSession = true
	projected.HasWorktree = true
	delta := userstore.ProjectDeltaState{ProjectID: projectID, Cursor: 1, Hash: "one", Initialized: true, Projector: issueProjectionProjector()}
	if err := rootStore.ReplaceProject(ctx, userstore.ProjectInput{ProjectID: projectID, Name: "P", Path: "/p", DBPath: "/p/db", Tasks: []domain.Task{projected}, Delta: &delta}); err != nil {
		t.Fatal(err)
	}
	materializer := newProjectReadMaterializer(projectID, nil, func(hydrateCtx context.Context, tasks []domain.Task) ([]domain.Task, error) {
		rows, listErr := runtimeStore.ListSessionStates(hydrateCtx, projectID)
		if listErr != nil {
			return nil, listErr
		}
		if len(rows) > 0 {
			tasks[0].Session = &domain.Session{IssueID: naming.IssueID(issueID), State: domain.SessionBusy, UpdatedAt: rows[0].UpdatedAt}
			tasks[0].HasTmuxSession = true
		}
		worktrees, listErr := runtimeStore.ListWorktreeStates(hydrateCtx, projectID)
		if listErr != nil {
			return nil, listErr
		}
		tasks[0].HasWorktree = len(worktrees) > 0
		return tasks, nil
	})
	canonicalByID := map[string]domain.Task{issueID: canonical}
	projectedByID := map[string]domain.Task{issueID: projected}
	issueKeys, runtimeKeys := checkpointMaterializedTasks(canonicalByID, projectedByID)
	materializer.replaceBootstrap(canonicalByID, projectedByID, protocol.MaterializedSnapshotMetadata{Health: "healthy"}, issueKeys, runtimeKeys)
	d := &Daemon{
		cfg: Config{RepoDir: "."}, userStore: rootStore,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{".": runtimeStore},
		materializers:       map[string]*projectReadMaterializer{projectID: materializer},
	}
	if err := newRuntimeProjectionWriter(d).ReplaceSessionProjectionSnapshot(ctx, projectID, nil); err != nil {
		t.Fatal(err)
	}
	if err := newRuntimeProjectionWriter(d).ReplaceWorktreeProjectionSnapshot(ctx, projectID, nil); err != nil {
		t.Fatal(err)
	}
	snapshot, err := rootStore.Snapshot(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Projects) != 1 || len(snapshot.Projects[0].Tasks) != 1 || snapshot.Projects[0].Tasks[0].Session != nil || snapshot.Projects[0].Tasks[0].HasTmuxSession || snapshot.Projects[0].Tasks[0].HasWorktree {
		t.Fatalf("removed runtime keys retained stale root current state: %+v", snapshot.Projects)
	}
}

func TestRuntimeProjectionWriterCoalescesProjectionBurstsByIssue(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessionStore := daemonstate.NewStore()
	runtimeStateStore := newRuntimeProjectionStore(t)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	const (
		projectID = "proj-coalesce"
		issueID   = "az-3"
		sessionID = "sess-3"
		worktree  = "/tmp/repo-az-3"
		branch    = "riordan/az-3/task"
	)
	if _, err := sessionStore.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed session store: %v", err)
	}

	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: logger},
		hub:          publish.NewHub(16, 8, logger),
		sessionStore: sessionStore,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}
	// Leave enough headroom for the SQLite persistence performed before each
	// schedule call when the cold suite is running packages concurrently. The
	// test is about debounce semantics, not a 100 ms latency budget.
	d.runtimeProjectionCoalescer = newRuntimeProjectionEventCoalescer(d, 500*time.Millisecond)
	defer d.runtimeProjectionCoalescer.Close()
	writer := newRuntimeProjectionWriter(d)

	ch, cancel := d.hub.Subscribe(projectID, 0)
	defer cancel()

	if rev, err := writer.PersistWorktreeProjectionAndPublish(ctx, projectID, issueID, worktree, branch); err != nil || rev != 0 {
		t.Fatalf("scheduled worktree revision = %d, want 0 before delayed publish", rev)
	}
	for i := 1; i <= 8; i++ {
		status := &git.GitStatus{
			Modified:       []string{"changed.go"},
			HasChanges:     true,
			GitAdditions:   i,
			GitDeletions:   i + 1,
			GitAheadCount:  i + 2,
			GitBehindCount: i + 3,
		}
		if rev, err := writer.PersistGitStatusProjectionAndPublish(ctx, projectID, issueID, worktree, status, true, true); err != nil || rev != 0 {
			t.Fatalf("scheduled git revision %d = %d, want 0 before delayed publish", i, rev)
		}
		// Keep the burst active for longer than one coalescing window while each
		// update still arrives well within that window. Publication must wait for
		// the quiet period after the final update, not fire mid-burst.
		time.Sleep(20 * time.Millisecond)
	}

	evt := waitForRuntimeProjectionEvent(t, ch)
	if evt.Revision != 1 {
		t.Fatalf("event revision = %d, want 1", evt.Revision)
	}
	if evt.Event != protocol.EventGitStatusUpdated {
		t.Fatalf("event = %s, want %s", evt.Event, protocol.EventGitStatusUpdated)
	}
	var body protocol.ProjectionUpdateEventBody
	if err := json.Unmarshal(evt.Body, &body); err != nil {
		t.Fatalf("unmarshal projection event: %v", err)
	}
	if body.Runtime == nil {
		t.Fatal("expected runtime projection body")
	}
	if body.Runtime.Projection.IssueID != issueID {
		t.Fatalf("runtime issue = %s, want %s", body.Runtime.Projection.IssueID, issueID)
	}
	if body.Runtime.Projection.Worktree.Path != worktree || body.Runtime.Projection.Session.SessionID != sessionID {
		t.Fatalf("runtime projection = %+v, want worktree/session %s/%s", body.Runtime.Projection, worktree, sessionID)
	}
	if body.Runtime.Projection.Git.GitAdditions != 8 || body.Runtime.Projection.Git.GitDeletions != 9 {
		t.Fatalf("runtime git stats = %+v, want final additions/deletions 8/9", body.Runtime.Projection.Git)
	}
	if body.Runtime.Projection.Git.GitAheadCount != 10 || body.Runtime.Projection.Git.GitBehindCount != 11 {
		t.Fatalf("runtime git ahead/behind = %+v, want final 10/11", body.Runtime.Projection.Git)
	}

	assertNoRuntimeProjectionEvent(t, ch, 50*time.Millisecond)
}

func TestRuntimeProjectionCoalescingDoesNotDelayNonProjectionEvents(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runtimeStateStore := newRuntimeProjectionStore(t)
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	const (
		projectID = "proj-coalesce-immediate"
		issueID   = "az-4"
		worktree  = "/tmp/repo-az-4"
	)
	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: logger},
		hub: publish.NewHub(16, 8, logger),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}
	d.runtimeProjectionCoalescer = newRuntimeProjectionEventCoalescer(d, 50*time.Millisecond)
	defer d.runtimeProjectionCoalescer.Close()
	writer := newRuntimeProjectionWriter(d)

	ch, cancel := d.hub.Subscribe(projectID, 0)
	defer cancel()

	writer.PersistWorktreeProjectionAndPublish(ctx, projectID, issueID, worktree, "branch")
	uiRev := d.nextRevision(projectID)
	d.hub.Publish(protocol.EventEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		ProjectID:       naming.ProjectID(protocol.NormalizeProjectID(projectID)),
		Revision:        uiRev,
		Event:           protocol.EventUICommandRequested,
		Kind:            protocol.EnvelopeKindEvent,
		EmittedAt:       time.Now().UTC(),
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(protocol.NormalizeProjectID(projectID))},
	})

	first := waitForRuntimeProjectionEvent(t, ch)
	if first.Event != protocol.EventUICommandRequested || first.Revision != 1 {
		t.Fatalf("first event = %s/%d, want immediate ui command revision 1", first.Event, first.Revision)
	}
	second := waitForRuntimeProjectionEvent(t, ch)
	if second.Event != protocol.EventWorktreeProjectionUpdated || second.Revision != 2 {
		t.Fatalf("second event = %s/%d, want coalesced projection revision 2", second.Event, second.Revision)
	}
}

func assertNoRuntimeProjectionEvent(t *testing.T, ch <-chan protocol.EventEnvelope, timeout time.Duration) {
	t.Helper()
	select {
	case evt := <-ch:
		t.Fatalf("unexpected extra projection event: %s revision %d", evt.Event, evt.Revision)
	case <-time.After(timeout):
	}
}
