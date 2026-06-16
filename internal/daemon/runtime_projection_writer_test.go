package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

type recordingRuntimeProjectionWriter struct {
	mu    sync.Mutex
	calls []string
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

func (r *recordingRuntimeProjectionWriter) PersistSessionProjection(context.Context, string, daemonstate.Session) error {
	r.record("session.persist")
	return nil
}

func (r *recordingRuntimeProjectionWriter) PersistSessionProjectionAndPublish(context.Context, string, protocol.Metadata, daemonstate.Session) uint64 {
	r.record("session.persist+publish")
	return 1
}

func (r *recordingRuntimeProjectionWriter) PublishSessionProjectionEvent(context.Context, string, protocol.Metadata, daemonstate.Session) uint64 {
	r.record("session.publish")
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

func (r *recordingRuntimeProjectionWriter) PersistWorktreeProjectionAndPublish(context.Context, string, string, string, string) uint64 {
	r.record("worktree.persist+publish")
	return 3
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

func (r *recordingRuntimeProjectionWriter) PersistGitStatusProjectionAndPublish(context.Context, string, string, string, *git.GitStatus, bool, bool) uint64 {
	r.record("git.persist+publish")
	return 6
}

func (r *recordingRuntimeProjectionWriter) PublishGitStatusProjectionEvent(context.Context, string, string, string, *git.GitStatus) uint64 {
	r.record("git.publish")
	return 7
}

type statusRunner struct {
	status string
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
	if err := d.persistTmuxSessionRuntimeState(ctx, projectID, []tmux.SessionInfo{{Name: sessionID}}); err != nil {
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

	rev := writer.PersistSessionProjectionAndPublish(ctx, projectID, protocol.Metadata{ProjectID: projectID}, session)
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
	d.runtimeProjectionCoalescer = newRuntimeProjectionEventCoalescer(d, 100*time.Millisecond)
	defer d.runtimeProjectionCoalescer.Close()
	writer := newRuntimeProjectionWriter(d)

	ch, cancel := d.hub.Subscribe(projectID, 0)
	defer cancel()

	if rev := writer.PersistWorktreeProjectionAndPublish(ctx, projectID, issueID, worktree, branch); rev != 0 {
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
		if rev := writer.PersistGitStatusProjectionAndPublish(ctx, projectID, issueID, worktree, status, true, true); rev != 0 {
			t.Fatalf("scheduled git revision %d = %d, want 0 before delayed publish", i, rev)
		}
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
