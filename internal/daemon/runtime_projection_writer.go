package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

type runtimeProjectionWriter interface {
	PersistSessionProjection(context.Context, string, daemonstate.Session) error
	PersistSessionProjectionAndPublish(context.Context, string, protocol.Metadata, daemonstate.Session) uint64
	PublishSessionProjectionEvent(context.Context, string, protocol.Metadata, daemonstate.Session) uint64
	ReplaceSessionProjectionSnapshot(context.Context, string, []daemonstate.Session) error

	PersistWorktreeProjection(context.Context, string, string, string, string) error
	PersistWorktreeProjectionAndPublish(context.Context, string, string, string, string) uint64
	DeleteWorktreeProjectionAndPublish(context.Context, string, string) uint64
	PublishWorktreeProjectionEvent(context.Context, string, string, string) uint64
	ReplaceWorktreeProjectionSnapshot(context.Context, string, []daemonstate.WorktreeProjection) error

	PersistGitStatusProjectionAndPublish(context.Context, string, string, string, *git.GitStatus, bool, bool) uint64
	PublishGitStatusProjectionEvent(context.Context, string, string, string, *git.GitStatus) uint64
}

type daemonRuntimeProjectionWriter struct {
	d  *Daemon
	mu sync.Mutex
}

func newRuntimeProjectionWriter(d *Daemon) *daemonRuntimeProjectionWriter {
	return &daemonRuntimeProjectionWriter{d: d}
}

func (w *daemonRuntimeProjectionWriter) PersistSessionProjection(ctx context.Context, projectID string, session daemonstate.Session) error {
	if w == nil || w.d == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.d.persistSessionState(projectID, session)
	return nil
}

func (w *daemonRuntimeProjectionWriter) PersistSessionProjectionAndPublish(ctx context.Context, projectID string, meta protocol.Metadata, session daemonstate.Session) uint64 {
	if w == nil || w.d == nil {
		return 0
	}
	projectID = protocol.NormalizeProjectID(projectID)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.d.persistSessionState(projectID, session)
	rev := w.d.nextRevision(projectID)
	w.d.publishSessionProjectionEventAtRevision(ctx, projectID, meta, session, rev)
	return rev
}

func (w *daemonRuntimeProjectionWriter) PublishSessionProjectionEvent(ctx context.Context, projectID string, meta protocol.Metadata, session daemonstate.Session) uint64 {
	if w == nil || w.d == nil {
		return 0
	}
	projectID = protocol.NormalizeProjectID(projectID)
	w.mu.Lock()
	defer w.mu.Unlock()
	rev := w.d.nextRevision(projectID)
	w.d.publishSessionProjectionEventAtRevision(ctx, projectID, meta, session, rev)
	return rev
}

func (w *daemonRuntimeProjectionWriter) ReplaceSessionProjectionSnapshot(ctx context.Context, projectID string, sessions []daemonstate.Session) error {
	if w == nil || w.d == nil || w.d.sessionRuntimeStateStore() == nil {
		return nil
	}
	projectID = protocol.NormalizeProjectID(projectID)
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.d.sessionRuntimeStateStore().ReplaceSessionStates(ctx, projectID, sessions)
}

func (w *daemonRuntimeProjectionWriter) PersistWorktreeProjection(ctx context.Context, projectID, issueID, path, branch string) error {
	if w == nil || w.d == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.d.persistWorktreeState(ctx, projectID, issueID, path, branch)
}

func (w *daemonRuntimeProjectionWriter) PersistWorktreeProjectionAndPublish(ctx context.Context, projectID, issueID, path, branch string) uint64 {
	if w == nil || w.d == nil {
		return 0
	}
	projectID = protocol.NormalizeProjectID(projectID)
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.d.persistWorktreeState(ctx, projectID, issueID, path, branch)
	rev := w.d.nextRevision(projectID)
	w.d.publishWorktreeProjectionEventAtRevision(ctx, projectID, issueID, path, rev)
	return rev
}

func (w *daemonRuntimeProjectionWriter) DeleteWorktreeProjectionAndPublish(ctx context.Context, projectID, issueID string) uint64 {
	if w == nil || w.d == nil {
		return 0
	}
	projectID = protocol.NormalizeProjectID(projectID)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.d.worktreeRuntimeStateStore() != nil {
		if err := w.d.worktreeRuntimeStateStore().DeleteWorktreeState(ctx, protocol.NormalizeProjectID(projectID), strings.TrimSpace(issueID)); err != nil && w.d.cfg.Logger != nil {
			w.d.cfg.Logger.Warn("delete worktree runtime state failed", "project_id", projectID, "issue_id", issueID, "error", err)
		}
	}
	rev := w.d.nextRevision(projectID)
	w.d.publishWorktreeProjectionEventAtRevision(ctx, projectID, issueID, "", rev)
	return rev
}

func (w *daemonRuntimeProjectionWriter) PublishWorktreeProjectionEvent(ctx context.Context, projectID, issueID, path string) uint64 {
	if w == nil || w.d == nil {
		return 0
	}
	projectID = protocol.NormalizeProjectID(projectID)
	w.mu.Lock()
	defer w.mu.Unlock()
	rev := w.d.nextRevision(projectID)
	w.d.publishWorktreeProjectionEventAtRevision(ctx, projectID, issueID, path, rev)
	return rev
}

func (w *daemonRuntimeProjectionWriter) ReplaceWorktreeProjectionSnapshot(ctx context.Context, projectID string, rows []daemonstate.WorktreeProjection) error {
	if w == nil || w.d == nil || w.d.worktreeRuntimeStateStore() == nil {
		return nil
	}
	projectID = protocol.NormalizeProjectID(projectID)
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.d.worktreeRuntimeStateStore().ReplaceWorktreeStates(ctx, projectID, rows)
}

func (w *daemonRuntimeProjectionWriter) PersistGitStatusProjectionAndPublish(
	ctx context.Context,
	projectID, issueID, worktree string,
	status *git.GitStatus,
	publishOnChange, forcePublish bool,
) uint64 {
	if w == nil || w.d == nil || w.d.worktreeRuntimeStateStore() == nil || status == nil {
		return 0
	}
	projectID = protocol.NormalizeProjectID(projectID)
	issueID = strings.TrimSpace(issueID)
	w.mu.Lock()
	defer w.mu.Unlock()
	worktree = strings.TrimSpace(worktree)
	if issueID == "" && worktree == "" {
		return 0
	}
	var (
		projection daemonstate.WorktreeProjection
		found      bool
		err        error
	)
	if issueID != "" {
		projection, found, err = w.d.worktreeRuntimeStateStore().GetWorktreeStateByIssueID(ctx, projectID, issueID)
	}
	if err != nil || !found {
		if worktree == "" {
			return 0
		}
		projection, found, err = w.d.worktreeRuntimeStateStore().GetWorktreeStateByPath(ctx, projectID, worktree)
	}
	if err != nil || !found || strings.TrimSpace(projection.IssueID) == "" {
		return 0
	}
	rawStatus, err := json.Marshal(status)
	if err != nil {
		return 0
	}
	changed := string(rawStatus) != string(projection.GitStatusRaw)
	if err := w.d.worktreeRuntimeStateStore().UpsertWorktreeStateGitStatus(ctx, projectID, projection.IssueID, rawStatus, time.Now().UTC()); err != nil {
		if w.d.cfg.Logger != nil {
			w.d.cfg.Logger.Debug("persist worktree runtime-state git status failed", "project_id", projectID, "issue_id", projection.IssueID, "worktree", worktree, "error", err)
		}
		return 0
	}
	if !(forcePublish || (publishOnChange && changed)) {
		return 0
	}
	rev := w.d.nextRevision(projectID)
	w.d.publishGitStatusProjectionEventAtRevision(ctx, projectID, projection.IssueID, worktree, status, rev)
	return rev
}

func (w *daemonRuntimeProjectionWriter) PublishGitStatusProjectionEvent(ctx context.Context, projectID, issueID, worktree string, status *git.GitStatus) uint64 {
	if w == nil || w.d == nil {
		return 0
	}
	projectID = protocol.NormalizeProjectID(projectID)
	w.mu.Lock()
	defer w.mu.Unlock()
	rev := w.d.nextRevision(projectID)
	w.d.publishGitStatusProjectionEventAtRevision(ctx, projectID, issueID, worktree, status, rev)
	return rev
}
