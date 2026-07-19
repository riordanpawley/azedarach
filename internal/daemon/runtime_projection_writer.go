package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/latencytrace"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

type runtimeProjectionWriter interface {
	ApplyPhysicalSessionObservationAndPublish(context.Context, string, protocol.Metadata, daemonstate.PhysicalSessionObservation) ([]daemonstate.Session, bool, []uint64, error)
	PersistSessionProjection(context.Context, string, daemonstate.Session) error
	PersistSessionProjectionAndPublish(context.Context, string, protocol.Metadata, daemonstate.Session) (uint64, error)
	PublishSessionProjectionEvent(context.Context, string, protocol.Metadata, daemonstate.Session) (uint64, error)
	ReplaceSessionProjectionSnapshot(context.Context, string, []daemonstate.Session) error

	PersistWorktreeProjection(context.Context, string, string, string, string) error
	PersistWorktreeProjectionAndPublish(context.Context, string, string, string, string) (uint64, error)
	DeleteWorktreeProjectionAndPublish(context.Context, string, string) (uint64, error)
	PublishWorktreeProjectionEvent(context.Context, string, string, string) (uint64, error)
	ReplaceWorktreeProjectionSnapshot(context.Context, string, []daemonstate.WorktreeState) error

	PersistGitStatusProjectionAndPublish(context.Context, string, string, string, *git.GitStatus, bool, bool) (uint64, error)
	PersistGitHookStatusProjectionAndPublishResult(context.Context, string, string, string, int64, *git.GitStatus) (uint64, error)
	PublishGitStatusProjectionEvent(context.Context, string, string, string, *git.GitStatus) (uint64, error)
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
	_, _, _, err := d.runtimeProjectionStateWriter().ApplyPhysicalSessionObservationAndPublish(ctx, projectID, meta, daemonstate.PhysicalSessionObservation{
		ProjectID: projectID, SessionID: session.ID, ObservedState: session.ObservedState,
		Activity: activity, ActivitySource: source, UpdatedAt: observedAt,
	})
	return err
}

type runtimeProjectionWriterOperationContextKey struct{}

func newRuntimeProjectionWriter(d *Daemon) *daemonRuntimeProjectionWriter {
	return &daemonRuntimeProjectionWriter{d: d}
}

func (d *Daemon) applyPhysicalSessionObservationWithProjectionCleanup(ctx context.Context, store *daemonstate.RuntimeStateStore, projectID, canonicalProjectID string, observation daemonstate.PhysicalSessionObservation) ([]daemonstate.Session, bool, error) {
	changed, applied, err := store.ApplyPhysicalSessionObservation(ctx, observation)
	if err == nil && applied && daemonstate.NormalizeSessionState(observation.ObservedState) == daemonstate.SessionStateStopped {
		d.purgeManagedAgentIdentityProjectionForSession(projectID, observation.SessionID)
		if canonicalProjectID = protocol.NormalizeProjectID(canonicalProjectID); canonicalProjectID != protocol.NormalizeProjectID(projectID) {
			d.purgeManagedAgentIdentityProjectionForSession(canonicalProjectID, observation.SessionID)
		}
	}
	return changed, applied, err
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

func withRuntimeProjectionWriterQueuedHookForTest(ctx context.Context, hook func(string)) context.Context {
	return withContextOperationLockQueuedHookForTest(ctx, hook)
}

func (w *daemonRuntimeProjectionWriter) lockProjectionWriter(ctx context.Context, projectID, operation string) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	operation = runtimeProjectionWriterOperationFromContext(ctx, operation)
	waitStartedAt := time.Now()
	holderOperation, err := w.mu.acquire(ctx, operation)
	latencytrace.LogPhaseContext(ctx, w.d.cfg.Logger, "daemon", "runtime_projection.writer_lock_wait", waitStartedAt,
		"project_id", projectID,
		"operation", operation,
		"writer.waiter_operation", operation,
		"writer.holder_operation", holderOperation,
	)
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

func (w *daemonRuntimeProjectionWriter) ApplyPhysicalSessionObservationAndPublish(ctx context.Context, projectID string, meta protocol.Metadata, observation daemonstate.PhysicalSessionObservation) ([]daemonstate.Session, bool, []uint64, error) {
	if w == nil || w.d == nil {
		return nil, false, nil, nil
	}
	projectID = w.d.canonicalProjectID(projectID)
	store := w.d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		return nil, false, nil, nil
	}
	operation := "session.observe_publish"
	unlock, err := w.lockProjectionWriter(ctx, projectID, operation)
	if err != nil {
		return nil, false, nil, err
	}
	persistStartedAt := time.Now()
	changed, applied, err := w.d.applyPhysicalSessionObservationWithProjectionCleanup(ctx, store, projectID, projectID, observation)
	revisions := make([]uint64, len(changed))
	if err == nil && applied && w.d.runtimeProjectionCoalescer == nil {
		for i := range changed {
			revisions[i] = w.d.nextRevision(projectID)
		}
	}
	unlock()
	w.logPhase(ctx, projectID, operation, "persist", persistStartedAt, err)
	if err != nil || !applied {
		return changed, applied, nil, err
	}
	for i, row := range changed {
		w.d.refreshProjectReadRuntime(ctx, projectID, row.IssueID)
		if w.d.runtimeProjectionCoalescer != nil {
			revisions[i] = w.d.runtimeProjectionCoalescer.ScheduleSession(ctx, projectID, meta, row)
		} else {
			w.d.publishSessionProjectionEventAtRevision(ctx, projectID, meta, row, revisions[i])
		}
	}
	return changed, true, revisions, nil
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

func (w *daemonRuntimeProjectionWriter) PersistSessionProjectionAndPublish(ctx context.Context, projectID string, meta protocol.Metadata, session daemonstate.Session) (uint64, error) {
	if w == nil || w.d == nil {
		return 0, nil
	}
	projectID = w.d.canonicalProjectID(projectID)
	operation := "session.persist_publish"
	unlock, err := w.lockProjectionWriter(ctx, projectID, operation)
	if err != nil {
		return 0, err
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
		return 0, err
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
	return rev, nil
}

func (w *daemonRuntimeProjectionWriter) PublishSessionProjectionEvent(ctx context.Context, projectID string, meta protocol.Metadata, session daemonstate.Session) (uint64, error) {
	if w == nil || w.d == nil {
		return 0, nil
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
			return 0, err
		}
		rev = w.d.nextRevision(projectID)
		unlock()
		w.d.publishSessionProjectionEventAtRevision(ctx, projectID, meta, session, rev)
	}
	w.logPhase(ctx, projectID, operation, "publish", publishStartedAt, nil)
	return rev, nil
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

func (w *daemonRuntimeProjectionWriter) PersistWorktreeProjectionAndPublish(ctx context.Context, projectID, issueID, path, branch string) (uint64, error) {
	if w == nil || w.d == nil {
		return 0, nil
	}
	projectID = w.d.canonicalProjectID(projectID)
	operation := "worktree.persist_publish"
	unlock, err := w.lockProjectionWriter(ctx, projectID, operation)
	if err != nil {
		return 0, err
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
		return 0, err
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
	return rev, nil
}

func (w *daemonRuntimeProjectionWriter) DeleteWorktreeProjectionAndPublish(ctx context.Context, projectID, issueID string) (uint64, error) {
	if w == nil || w.d == nil {
		return 0, nil
	}
	projectID = w.d.canonicalProjectID(projectID)
	operation := "worktree.delete_publish"
	unlock, err := w.lockProjectionWriter(ctx, projectID, operation)
	if err != nil {
		return 0, err
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
	if persistErr != nil {
		return 0, persistErr
	}
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
	return rev, nil
}

func (w *daemonRuntimeProjectionWriter) PublishWorktreeProjectionEvent(ctx context.Context, projectID, issueID, path string) (uint64, error) {
	if w == nil || w.d == nil {
		return 0, nil
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
			return 0, err
		}
		rev = w.d.nextRevision(projectID)
		unlock()
		w.d.publishWorktreeProjectionEventAtRevision(ctx, projectID, issueID, path, rev)
	}
	w.logPhase(ctx, projectID, operation, "publish", publishStartedAt, nil)
	return rev, nil
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
	unchanged := err == nil && sameWorktreeProjectionIdentity(previous, rows)
	if err == nil {
		if unchanged {
			err = store.TouchWorktreeStates(ctx, projectID, latestWorktreeObservation(rows))
		} else {
			err = store.ReplaceWorktreeStates(ctx, projectID, rows)
		}
	}
	unlock()
	w.logPhase(ctx, projectID, operation, "persist", persistStartedAt, err)
	if err == nil && !unchanged {
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

func sameWorktreeProjectionIdentity(left, right []daemonstate.WorktreeState) bool {
	if len(left) != len(right) {
		return false
	}
	identities := make(map[string]struct {
		path   string
		branch string
	}, len(left))
	for _, row := range left {
		issueID := strings.TrimSpace(row.IssueID)
		if issueID == "" {
			return false
		}
		identities[issueID] = struct {
			path   string
			branch string
		}{path: row.Path, branch: row.Branch}
	}
	if len(identities) != len(right) {
		return false
	}
	seen := make(map[string]struct{}, len(right))
	for _, row := range right {
		issueID := strings.TrimSpace(row.IssueID)
		if _, duplicate := seen[issueID]; duplicate {
			return false
		}
		seen[issueID] = struct{}{}
		identity, ok := identities[issueID]
		if !ok || identity.path != row.Path || identity.branch != row.Branch {
			return false
		}
	}
	return true
}

func latestWorktreeObservation(rows []daemonstate.WorktreeState) time.Time {
	var latest time.Time
	for _, row := range rows {
		if row.UpdatedAt.After(latest) {
			latest = row.UpdatedAt
		}
	}
	return latest
}

func (w *daemonRuntimeProjectionWriter) PersistGitStatusProjectionAndPublish(
	ctx context.Context,
	projectID, issueID, worktree string,
	status *git.GitStatus,
	publishOnChange, forcePublish bool,
) (uint64, error) {
	if w == nil || w.d == nil || w.d.worktreeRuntimeStateStore(projectID) == nil || status == nil {
		return 0, nil
	}
	projectID = w.d.canonicalProjectID(projectID)
	issueID = strings.TrimSpace(issueID)
	operation := "git_status.persist_publish"
	worktree = strings.TrimSpace(worktree)
	if issueID == "" && worktree == "" {
		return 0, nil
	}
	rawStatus, err := json.Marshal(status)
	if err != nil {
		if w.d.cfg.Logger != nil {
			w.d.cfg.Logger.Warn("marshal worktree runtime-state git status failed", "project_id", projectID, "issue_id", issueID, "worktree", worktree, "error", err)
		}
		return 0, err
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
		return 0, persistErr
	}
	if !changed && !forcePublish {
		return 0, nil
	}
	refreshStartedAt := time.Now()
	w.d.refreshProjectReadRuntime(ctx, projectID, projection.IssueID)
	w.logPhase(ctx, projectID, operation, "refresh", refreshStartedAt, nil)
	if !(forcePublish || (publishOnChange && changed)) {
		return 0, nil
	}
	publishStartedAt := time.Now()
	if w.d.runtimeProjectionCoalescer != nil {
		rev = w.d.runtimeProjectionCoalescer.ScheduleGitStatus(ctx, projectID, projection.IssueID, worktree, status)
	} else {
		w.d.publishGitStatusProjectionEventAtRevision(ctx, projectID, projection.IssueID, worktree, status, rev)
	}
	w.logPhase(ctx, projectID, operation, "publish", publishStartedAt, nil)
	return rev, nil
}

func (w *daemonRuntimeProjectionWriter) PersistGitHookStatusProjectionAndPublishResult(
	ctx context.Context,
	projectID, issueID, worktree string,
	generation int64,
	status *git.GitStatus,
) (uint64, error) {
	if w == nil || w.d == nil || status == nil {
		return 0, fmt.Errorf("git hook status projection writer unavailable")
	}
	projectID = w.d.canonicalProjectID(projectID)
	store := w.d.worktreeRuntimeStateStore(projectID)
	if store == nil {
		return 0, fmt.Errorf("git hook runtime state store unavailable")
	}
	issueID = strings.TrimSpace(issueID)
	worktree = strings.TrimSpace(worktree)
	rawStatus, err := json.Marshal(status)
	if err != nil {
		return 0, err
	}
	operation := "git_status.hook_persist_publish"
	unlock, err := w.lockProjectionWriter(ctx, projectID, operation)
	if err != nil {
		return 0, err
	}
	persistStartedAt := time.Now()
	published, persistErr := store.PersistGitHookRefreshPublication(ctx, projectID, issueID, worktree, generation, rawStatus, time.Now().UTC())
	var rev uint64
	if published && w.d.runtimeProjectionCoalescer == nil {
		rev = w.d.nextRevision(projectID)
	}
	unlock()
	w.logPhase(ctx, projectID, operation, "persist", persistStartedAt, persistErr)
	if persistErr != nil {
		return 0, persistErr
	}
	if !published {
		return 0, nil
	}
	w.d.refreshProjectReadRuntime(ctx, projectID, issueID)
	if hook := gitHookPublicationCommittedHookFromContext(ctx); hook != nil {
		if err := hook(); err != nil {
			return 0, err
		}
	}
	if w.d.runtimeProjectionCoalescer != nil {
		rev = w.d.runtimeProjectionCoalescer.ScheduleGitStatus(ctx, projectID, issueID, worktree, status)
	} else {
		w.d.publishGitStatusProjectionEventAtRevision(ctx, projectID, issueID, worktree, status, rev)
	}
	return rev, nil
}

type gitHookPublicationCommittedHookKey struct{}

func withGitHookPublicationCommittedHookForTest(ctx context.Context, hook func() error) context.Context {
	return context.WithValue(ctx, gitHookPublicationCommittedHookKey{}, hook)
}

func gitHookPublicationCommittedHookFromContext(ctx context.Context) func() error {
	if ctx == nil {
		return nil
	}
	hook, _ := ctx.Value(gitHookPublicationCommittedHookKey{}).(func() error)
	return hook
}

func (w *daemonRuntimeProjectionWriter) PublishGitStatusProjectionEvent(ctx context.Context, projectID, issueID, worktree string, status *git.GitStatus) (uint64, error) {
	if w == nil || w.d == nil {
		return 0, nil
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
			return 0, err
		}
		rev = w.d.nextRevision(projectID)
		unlock()
		w.d.publishGitStatusProjectionEventAtRevision(ctx, projectID, issueID, worktree, status, rev)
	}
	w.logPhase(ctx, projectID, operation, "publish", publishStartedAt, nil)
	return rev, nil
}
