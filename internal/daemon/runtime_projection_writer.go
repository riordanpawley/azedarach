package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/latencytrace"
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
	ReplaceWorktreeProjectionSnapshot(context.Context, string, []daemonstate.WorktreeState) error

	PersistGitStatusProjectionAndPublish(context.Context, string, string, string, *git.GitStatus, bool, bool) uint64
	PublishGitStatusProjectionEvent(context.Context, string, string, string, *git.GitStatus) uint64
}

type daemonRuntimeProjectionWriter struct {
	d  *Daemon
	mu contextOperationLock
}

func (d *Daemon) persistObservedRuntimeProjection(ctx context.Context, projectID string, meta protocol.Metadata, session daemonstate.Session) error {
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		return nil
	}
	observedAt := session.UpdatedAt
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	activity, source := session.Activity, session.ActivitySource
	if daemonstate.NormalizeSessionState(session.ObservedState) == daemonstate.SessionStateStopped {
		activity, source = "", ""
	}
	changed, _, err := store.ApplyPhysicalSessionObservation(ctx, daemonstate.PhysicalSessionObservation{
		ProjectID: projectID, SessionID: session.ID, ObservedState: session.ObservedState,
		Activity: activity, ActivitySource: source, UpdatedAt: observedAt,
	})
	if err != nil {
		return err
	}
	writer := d.runtimeProjectionStateWriter()
	for _, row := range changed {
		writer.PublishSessionProjectionEvent(ctx, projectID, meta, row)
	}
	return nil
}

type runtimeProjectionWriterOperationContextKey struct{}

func newRuntimeProjectionWriter(d *Daemon) *daemonRuntimeProjectionWriter {
	return &daemonRuntimeProjectionWriter{d: d}
}

func contextWithRuntimeProjectionWriterOperation(ctx context.Context, operation string) context.Context {
	operation = strings.TrimSpace(operation)
	if operation == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, runtimeProjectionWriterOperationContextKey{}, operation)
}

func runtimeProjectionWriterOperationFromContext(ctx context.Context, fallback string) string {
	if ctx != nil {
		if operation, ok := ctx.Value(runtimeProjectionWriterOperationContextKey{}).(string); ok {
			if operation = strings.TrimSpace(operation); operation != "" {
				return operation
			}
		}
	}
	return fallback
}

func withRuntimeProjectionWriterWaitHookForTest(ctx context.Context, hook func(string, string)) context.Context {
	return withContextOperationLockWaitHookForTest(ctx, hook)
}

func (w *daemonRuntimeProjectionWriter) lockProjectionWriter(ctx context.Context, projectID, operation string) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	operation = runtimeProjectionWriterOperationFromContext(ctx, operation)
	waitStartedAt := time.Now()
	holderOperation, err := w.mu.acquire(ctx, operation)
	latencytrace.LogPhaseContext(ctx, w.d.cfg.Logger, "daemon", "runtime_projection.writer_lock_wait", waitStartedAt, "project_id", projectID, "operation", operation, "holder_operation", holderOperation)
	if err != nil {
		return nil, err
	}
	holdStartedAt := time.Now()
	return func() {
		latencytrace.LogPhaseContext(ctx, w.d.cfg.Logger, "daemon", "runtime_projection.writer_lock_held", holdStartedAt, "project_id", projectID, "operation", operation)
		w.mu.release()
	}, nil
}

func (w *daemonRuntimeProjectionWriter) logPhase(ctx context.Context, projectID, operation, phase string, startedAt time.Time, err error) {
	attrs := []any{"project_id", projectID, "operation", runtimeProjectionWriterOperationFromContext(ctx, operation)}
	if err != nil {
		attrs = append(attrs, "error", err)
	}
	latencytrace.LogPhaseContext(ctx, w.d.cfg.Logger, "daemon", "runtime_projection.writer_"+phase, startedAt, attrs...)
}

func (w *daemonRuntimeProjectionWriter) PersistSessionProjection(ctx context.Context, projectID string, session daemonstate.Session) error {
	if w == nil || w.d == nil {
		return nil
	}
	projectID = w.d.canonicalProjectID(projectID)
	operation := "session.persist"
	unlock, err := w.lockProjectionWriter(ctx, projectID, operation)
	if err != nil {
		return err
	}
	persistStartedAt := time.Now()
	err = w.d.persistSessionState(projectID, session)
	unlock()
	w.logPhase(ctx, projectID, operation, "persist", persistStartedAt, err)
	if err == nil {
		refreshStartedAt := time.Now()
		w.d.refreshProjectReadRuntime(ctx, projectID, session.IssueID)
		w.logPhase(ctx, projectID, operation, "refresh", refreshStartedAt, nil)
	}
	return err
}

func (w *daemonRuntimeProjectionWriter) PersistSessionProjectionAndPublish(ctx context.Context, projectID string, meta protocol.Metadata, session daemonstate.Session) uint64 {
	if w == nil || w.d == nil {
		return 0
	}
	projectID = w.d.canonicalProjectID(projectID)
	operation := "session.persist_publish"
	unlock, err := w.lockProjectionWriter(ctx, projectID, operation)
	if err != nil {
		return 0
	}
	persistStartedAt := time.Now()
	err = w.d.persistSessionState(projectID, session)
	var rev uint64
	if err == nil && w.d.runtimeProjectionCoalescer == nil {
		rev = w.d.nextRevision(projectID)
	}
	unlock()
	w.logPhase(ctx, projectID, operation, "persist", persistStartedAt, err)
	if err != nil {
		return 0
	}
	refreshStartedAt := time.Now()
	w.d.refreshProjectReadRuntime(ctx, projectID, session.IssueID)
	w.logPhase(ctx, projectID, operation, "refresh", refreshStartedAt, nil)
	publishStartedAt := time.Now()
	if w.d.runtimeProjectionCoalescer != nil {
		rev = w.d.runtimeProjectionCoalescer.ScheduleSession(ctx, projectID, meta, session)
	} else {
		w.d.publishSessionProjectionEventAtRevision(ctx, projectID, meta, session, rev)
	}
	w.logPhase(ctx, projectID, operation, "publish", publishStartedAt, nil)
	return rev
}

func (w *daemonRuntimeProjectionWriter) PublishSessionProjectionEvent(ctx context.Context, projectID string, meta protocol.Metadata, session daemonstate.Session) uint64 {
	if w == nil || w.d == nil {
		return 0
	}
	projectID = w.d.canonicalProjectID(projectID)
	operation := "session.publish"
	refreshStartedAt := time.Now()
	w.d.refreshProjectReadRuntime(ctx, projectID, session.IssueID)
	w.logPhase(ctx, projectID, operation, "refresh", refreshStartedAt, nil)
	publishStartedAt := time.Now()
	var rev uint64
	if w.d.runtimeProjectionCoalescer != nil {
		rev = w.d.runtimeProjectionCoalescer.ScheduleSession(ctx, projectID, meta, session)
	} else {
		unlock, err := w.lockProjectionWriter(ctx, projectID, operation)
		if err != nil {
			return 0
		}
		rev = w.d.nextRevision(projectID)
		unlock()
		w.d.publishSessionProjectionEventAtRevision(ctx, projectID, meta, session, rev)
	}
	w.logPhase(ctx, projectID, operation, "publish", publishStartedAt, nil)
	return rev
}

func (w *daemonRuntimeProjectionWriter) ReplaceSessionProjectionSnapshot(ctx context.Context, projectID string, sessions []daemonstate.Session) error {
	if w == nil || w.d == nil || w.d.sessionRuntimeStateStore(projectID) == nil {
		return nil
	}
	projectID = w.d.canonicalProjectID(projectID)
	operation := "session.replace_snapshot"
	store := w.d.sessionRuntimeStateStore(projectID)
	unlock, err := w.lockProjectionWriter(ctx, projectID, operation)
	if err != nil {
		return err
	}
	persistStartedAt := time.Now()
	previous, err := store.ListSessionStates(ctx, projectID)
	if err == nil {
		err = store.ReplaceSessionStates(ctx, projectID, sessions)
	}
	unlock()
	w.logPhase(ctx, projectID, operation, "persist", persistStartedAt, err)
	if err == nil {
		issueIDs := make([]string, 0, len(previous)+len(sessions))
		for _, session := range previous {
			issueIDs = append(issueIDs, session.IssueID)
		}
		for _, session := range sessions {
			issueIDs = append(issueIDs, session.IssueID)
		}
		refreshStartedAt := time.Now()
		w.d.refreshProjectReadRuntime(ctx, projectID, issueIDs...)
		w.logPhase(ctx, projectID, operation, "refresh", refreshStartedAt, nil)
	}
	return err
}

func (w *daemonRuntimeProjectionWriter) PersistWorktreeProjection(ctx context.Context, projectID, issueID, path, branch string) error {
	if w == nil || w.d == nil {
		return nil
	}
	projectID = w.d.canonicalProjectID(projectID)
	operation := "worktree.persist"
	unlock, err := w.lockProjectionWriter(ctx, projectID, operation)
	if err != nil {
		return err
	}
	persistStartedAt := time.Now()
	err = w.d.persistWorktreeState(ctx, projectID, issueID, path, branch)
	unlock()
	w.logPhase(ctx, projectID, operation, "persist", persistStartedAt, err)
	if err == nil {
		refreshStartedAt := time.Now()
		w.d.refreshProjectReadRuntime(ctx, projectID, issueID)
		w.logPhase(ctx, projectID, operation, "refresh", refreshStartedAt, nil)
	}
	return err
}

func (w *daemonRuntimeProjectionWriter) PersistWorktreeProjectionAndPublish(ctx context.Context, projectID, issueID, path, branch string) uint64 {
	if w == nil || w.d == nil {
		return 0
	}
	projectID = w.d.canonicalProjectID(projectID)
	operation := "worktree.persist_publish"
	unlock, err := w.lockProjectionWriter(ctx, projectID, operation)
	if err != nil {
		return 0
	}
	persistStartedAt := time.Now()
	err = w.d.persistWorktreeState(ctx, projectID, issueID, path, branch)
	var rev uint64
	if err == nil && w.d.runtimeProjectionCoalescer == nil {
		rev = w.d.nextRevision(projectID)
	}
	unlock()
	w.logPhase(ctx, projectID, operation, "persist", persistStartedAt, err)
	if err != nil {
		return 0
	}
	refreshStartedAt := time.Now()
	w.d.refreshProjectReadRuntime(ctx, projectID, issueID)
	w.logPhase(ctx, projectID, operation, "refresh", refreshStartedAt, nil)
	publishStartedAt := time.Now()
	if w.d.runtimeProjectionCoalescer != nil {
		rev = w.d.runtimeProjectionCoalescer.ScheduleWorktree(ctx, projectID, issueID, path)
	} else {
		w.d.publishWorktreeProjectionEventAtRevision(ctx, projectID, issueID, path, rev)
	}
	w.logPhase(ctx, projectID, operation, "publish", publishStartedAt, nil)
	return rev
}

func (w *daemonRuntimeProjectionWriter) DeleteWorktreeProjectionAndPublish(ctx context.Context, projectID, issueID string) uint64 {
	if w == nil || w.d == nil {
		return 0
	}
	projectID = w.d.canonicalProjectID(projectID)
	operation := "worktree.delete_publish"
	unlock, err := w.lockProjectionWriter(ctx, projectID, operation)
	if err != nil {
		return 0
	}
	persistStartedAt := time.Now()
	var persistErr error
	if w.d.worktreeRuntimeStateStore(projectID) != nil {
		persistErr = w.d.worktreeRuntimeStateStore(projectID).DeleteWorktreeState(ctx, projectID, strings.TrimSpace(issueID))
		if persistErr != nil && w.d.cfg.Logger != nil {
			w.d.cfg.Logger.Warn("delete worktree runtime state failed", "project_id", projectID, "issue_id", issueID, "error", persistErr)
		}
	}
	var rev uint64
	if w.d.runtimeProjectionCoalescer == nil {
		rev = w.d.nextRevision(projectID)
	}
	unlock()
	w.logPhase(ctx, projectID, operation, "persist", persistStartedAt, persistErr)
	refreshStartedAt := time.Now()
	w.d.refreshProjectReadRuntime(ctx, projectID, issueID)
	w.logPhase(ctx, projectID, operation, "refresh", refreshStartedAt, nil)
	publishStartedAt := time.Now()
	if w.d.runtimeProjectionCoalescer != nil {
		rev = w.d.runtimeProjectionCoalescer.ScheduleWorktree(ctx, projectID, issueID, "")
	} else {
		w.d.publishWorktreeProjectionEventAtRevision(ctx, projectID, issueID, "", rev)
	}
	w.logPhase(ctx, projectID, operation, "publish", publishStartedAt, nil)
	return rev
}

func (w *daemonRuntimeProjectionWriter) PublishWorktreeProjectionEvent(ctx context.Context, projectID, issueID, path string) uint64 {
	if w == nil || w.d == nil {
		return 0
	}
	projectID = w.d.canonicalProjectID(projectID)
	operation := "worktree.publish"
	refreshStartedAt := time.Now()
	w.d.refreshProjectReadRuntime(ctx, projectID, issueID)
	w.logPhase(ctx, projectID, operation, "refresh", refreshStartedAt, nil)
	publishStartedAt := time.Now()
	var rev uint64
	if w.d.runtimeProjectionCoalescer != nil {
		rev = w.d.runtimeProjectionCoalescer.ScheduleWorktree(ctx, projectID, issueID, path)
	} else {
		unlock, err := w.lockProjectionWriter(ctx, projectID, operation)
		if err != nil {
			return 0
		}
		rev = w.d.nextRevision(projectID)
		unlock()
		w.d.publishWorktreeProjectionEventAtRevision(ctx, projectID, issueID, path, rev)
	}
	w.logPhase(ctx, projectID, operation, "publish", publishStartedAt, nil)
	return rev
}

func (w *daemonRuntimeProjectionWriter) ReplaceWorktreeProjectionSnapshot(ctx context.Context, projectID string, rows []daemonstate.WorktreeState) error {
	if w == nil || w.d == nil || w.d.worktreeRuntimeStateStore(projectID) == nil {
		return nil
	}
	projectID = w.d.canonicalProjectID(projectID)
	operation := "worktree.replace_snapshot"
	store := w.d.worktreeRuntimeStateStore(projectID)
	unlock, err := w.lockProjectionWriter(ctx, projectID, operation)
	if err != nil {
		return err
	}
	persistStartedAt := time.Now()
	previous, err := store.ListWorktreeStates(ctx, projectID)
	if err == nil {
		err = store.ReplaceWorktreeStates(ctx, projectID, rows)
	}
	unlock()
	w.logPhase(ctx, projectID, operation, "persist", persistStartedAt, err)
	if err == nil {
		issueIDs := make([]string, 0, len(previous)+len(rows))
		for _, row := range previous {
			issueIDs = append(issueIDs, row.IssueID)
		}
		for _, row := range rows {
			issueIDs = append(issueIDs, row.IssueID)
		}
		refreshStartedAt := time.Now()
		w.d.refreshProjectReadRuntime(ctx, projectID, issueIDs...)
		w.logPhase(ctx, projectID, operation, "refresh", refreshStartedAt, nil)
	}
	return err
}

func (w *daemonRuntimeProjectionWriter) PersistGitStatusProjectionAndPublish(
	ctx context.Context,
	projectID, issueID, worktree string,
	status *git.GitStatus,
	publishOnChange, forcePublish bool,
) uint64 {
	if w == nil || w.d == nil || w.d.worktreeRuntimeStateStore(projectID) == nil || status == nil {
		return 0
	}
	projectID = w.d.canonicalProjectID(projectID)
	issueID = strings.TrimSpace(issueID)
	operation := "git_status.persist_publish"
	worktree = strings.TrimSpace(worktree)
	if issueID == "" && worktree == "" {
		return 0
	}
	rawStatus, err := json.Marshal(status)
	if err != nil {
		if w.d.cfg.Logger != nil {
			w.d.cfg.Logger.Warn("marshal worktree runtime-state git status failed", "project_id", projectID, "issue_id", issueID, "worktree", worktree, "error", err)
		}
		return 0
	}
	var (
		projection daemonstate.WorktreeState
		found      bool
		changed    bool
		persisted  bool
		persistErr error
		rev        uint64
	)
	var persistStartedAt time.Time
	func() {
		unlock, lockErr := w.lockProjectionWriter(ctx, projectID, operation)
		if lockErr != nil {
			persistErr = lockErr
			return
		}
		defer unlock()
		persistStartedAt = time.Now()
		if issueID != "" {
			projection, found, persistErr = w.d.worktreeRuntimeStateStore(projectID).GetWorktreeStateByIssueID(ctx, projectID, issueID)
		}
		if persistErr != nil || !found {
			if persistErr != nil && w.d.cfg.Logger != nil {
				w.d.cfg.Logger.Warn("persist worktree runtime-state git status lookup by issue failed", "project_id", projectID, "issue_id", issueID, "error", persistErr)
			}
			if worktree == "" {
				return
			}
			projection, found, persistErr = w.d.worktreeRuntimeStateStore(projectID).GetWorktreeStateByPath(ctx, projectID, worktree)
		}
		if persistErr != nil || !found || strings.TrimSpace(projection.IssueID) == "" {
			if persistErr != nil && w.d.cfg.Logger != nil {
				w.d.cfg.Logger.Warn("persist worktree runtime-state git status lookup by path failed", "project_id", projectID, "worktree", worktree, "error", persistErr)
			}
			return
		}
		changed = string(rawStatus) != string(projection.GitStatusRaw)
		persistErr = w.d.worktreeRuntimeStateStore(projectID).UpsertWorktreeStateGitStatus(ctx, projectID, projection.IssueID, rawStatus, time.Now().UTC())
		if persistErr != nil {
			if w.d.cfg.Logger != nil {
				w.d.cfg.Logger.Warn("persist worktree runtime-state git status failed", "project_id", projectID, "issue_id", projection.IssueID, "worktree", worktree, "error", persistErr)
			}
			return
		}
		persisted = true
		if (forcePublish || (publishOnChange && changed)) && w.d.runtimeProjectionCoalescer == nil {
			rev = w.d.nextRevision(projectID)
		}
	}()
	w.logPhase(ctx, projectID, operation, "persist", persistStartedAt, persistErr)
	if !persisted {
		return 0
	}
	refreshStartedAt := time.Now()
	w.d.refreshProjectReadRuntime(ctx, projectID, projection.IssueID)
	w.logPhase(ctx, projectID, operation, "refresh", refreshStartedAt, nil)
	if !(forcePublish || (publishOnChange && changed)) {
		return 0
	}
	publishStartedAt := time.Now()
	if w.d.runtimeProjectionCoalescer != nil {
		rev = w.d.runtimeProjectionCoalescer.ScheduleGitStatus(ctx, projectID, projection.IssueID, worktree, status)
	} else {
		w.d.publishGitStatusProjectionEventAtRevision(ctx, projectID, projection.IssueID, worktree, status, rev)
	}
	w.logPhase(ctx, projectID, operation, "publish", publishStartedAt, nil)
	return rev
}

func (w *daemonRuntimeProjectionWriter) PublishGitStatusProjectionEvent(ctx context.Context, projectID, issueID, worktree string, status *git.GitStatus) uint64 {
	if w == nil || w.d == nil {
		return 0
	}
	projectID = w.d.canonicalProjectID(projectID)
	operation := "git_status.publish"
	refreshStartedAt := time.Now()
	w.d.refreshProjectReadRuntime(ctx, projectID, issueID)
	w.logPhase(ctx, projectID, operation, "refresh", refreshStartedAt, nil)
	publishStartedAt := time.Now()
	var rev uint64
	if w.d.runtimeProjectionCoalescer != nil {
		rev = w.d.runtimeProjectionCoalescer.ScheduleGitStatus(ctx, projectID, issueID, worktree, status)
	} else {
		unlock, err := w.lockProjectionWriter(ctx, projectID, operation)
		if err != nil {
			return 0
		}
		rev = w.d.nextRevision(projectID)
		unlock()
		w.d.publishGitStatusProjectionEventAtRevision(ctx, projectID, issueID, worktree, status, rev)
	}
	w.logPhase(ctx, projectID, operation, "publish", publishStartedAt, nil)
	return rev
}
