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
	"github.com/riordanpawley/azedarach/internal/services/git"
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

func (r *recordingRuntimeProjectionWriter) ReplaceWorktreeProjectionSnapshot(context.Context, string, []daemonstate.WorktreeProjection) error {
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
	projectionStore := newRuntimeProjectionStore(t)
	t.Cleanup(func() { _ = projectionStore.Close() })

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
	if err := projectionStore.UpsertWorktree(ctx, daemonstate.WorktreeProjection{
		ProjectID: projectID,
		IssueID:   issueID,
		Path:      worktree,
		Branch:    branch,
		UpdatedAt: time.Date(2026, time.April, 2, 11, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed worktree projection: %v", err)
	}

	writer := &recordingRuntimeProjectionWriter{}
	d := &Daemon{
		cfg:                     Config{Logger: logger},
		sessionStore:            sessionStore,
		projectionStore:         projectionStore,
		runtimeProjectionWriter: writer,
	}

	if err := d.upsertSessionAndPublish(projectID, sessionID, issueID, daemonstate.SessionStateStarting); err != nil {
		t.Fatalf("upsertSessionAndPublish: %v", err)
	}
	d.writeSessionStopProjection(projectID, sessionID, issueID)
	if err := d.persistTmuxSessionProjectionSnapshot(ctx, projectID, []string{sessionID}); err != nil {
		t.Fatalf("persistTmuxSessionProjectionSnapshot: %v", err)
	}

	wa := &worktreeServiceAdapter{
		runtimeProjectionWriter: writer,
		projectionStore:         projectionStore,
		logger:                  logger,
	}
	wa.writeWorktreeProjectionSnapshot(ctx, projectID, []git.Worktree{{IssueID: issueID, Path: worktree, Branch: branch}})

	ga := &gitServiceAdapter{
		client:                  git.NewClient(statusRunner{status: " M README.md\n"}, logger),
		projectionStore:         projectionStore,
		runtimeProjectionWriter: writer,
		logger:                  logger,
	}
	ga.refreshGitStatusWriteThrough(ctx, projectID, worktree, true, false)

	got := strings.Join(writer.snapshot(), ",")
	for _, want := range []string{
		"session.persist+publish",
		"session.persist",
		"session.snapshot.replace",
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
	projectionStore := newRuntimeProjectionStore(t)
	t.Cleanup(func() { _ = projectionStore.Close() })

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
		cfg:             Config{Logger: logger},
		hub:             publish.NewHub(8, 4, logger),
		sessionStore:    sessionStore,
		projectionStore: projectionStore,
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
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for session projection event")
	}

	sessions, err := projectionStore.ListSessions(ctx, projectID)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if got, want := len(sessions), 1; got != want {
		t.Fatalf("session row count = %d, want %d", got, want)
	}
	if sessions[0].ID != sessionID || sessions[0].IssueID != issueID {
		t.Fatalf("session row = %+v", sessions[0])
	}
}
