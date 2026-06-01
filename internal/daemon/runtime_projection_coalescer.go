package daemon

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

type runtimeProjectionEventKind uint8

const (
	runtimeProjectionEventSession runtimeProjectionEventKind = iota + 1
	runtimeProjectionEventWorktree
	runtimeProjectionEventGitStatus
)

type runtimeProjectionEventCoalescer struct {
	d      *Daemon
	window time.Duration

	mu      sync.Mutex
	pending map[runtimeProjectionEventKey]*pendingRuntimeProjectionEvent
	closed  bool
}

type runtimeProjectionEventKey struct {
	projectID string
	issueID   string
	fallback  string
}

type pendingRuntimeProjectionEvent struct {
	key      runtimeProjectionEventKey
	kind     runtimeProjectionEventKind
	meta     protocol.Metadata
	session  daemonstate.Session
	worktree string
	status   *git.GitStatus
	timer    *time.Timer
}

func newRuntimeProjectionEventCoalescer(d *Daemon, window time.Duration) *runtimeProjectionEventCoalescer {
	if window <= 0 {
		window = defaultRuntimeProjectionCoalesceWindow
	}
	return &runtimeProjectionEventCoalescer{
		d:       d,
		window:  window,
		pending: map[runtimeProjectionEventKey]*pendingRuntimeProjectionEvent{},
	}
}

func (c *runtimeProjectionEventCoalescer) ScheduleSession(ctx context.Context, projectID string, meta protocol.Metadata, session daemonstate.Session) uint64 {
	if c == nil || c.d == nil {
		return 0
	}
	projectID = c.d.canonicalProjectID(projectID)
	key := runtimeProjectionCoalesceKey(projectID, session.IssueID, session.ID)
	return c.schedule(ctx, &pendingRuntimeProjectionEvent{
		key:     key,
		kind:    runtimeProjectionEventSession,
		meta:    meta,
		session: session,
	})
}

func (c *runtimeProjectionEventCoalescer) ScheduleWorktree(ctx context.Context, projectID, issueID, worktree string) uint64 {
	if c == nil || c.d == nil {
		return 0
	}
	projectID = c.d.canonicalProjectID(projectID)
	key := runtimeProjectionCoalesceKey(projectID, issueID, worktree)
	return c.schedule(ctx, &pendingRuntimeProjectionEvent{
		key:      key,
		kind:     runtimeProjectionEventWorktree,
		worktree: strings.TrimSpace(worktree),
	})
}

func (c *runtimeProjectionEventCoalescer) ScheduleGitStatus(ctx context.Context, projectID, issueID, worktree string, status *git.GitStatus) uint64 {
	if c == nil || c.d == nil {
		return 0
	}
	projectID = c.d.canonicalProjectID(projectID)
	key := runtimeProjectionCoalesceKey(projectID, issueID, worktree)
	return c.schedule(ctx, &pendingRuntimeProjectionEvent{
		key:      key,
		kind:     runtimeProjectionEventGitStatus,
		worktree: strings.TrimSpace(worktree),
		status:   cloneGitStatus(status),
	})
}

func (c *runtimeProjectionEventCoalescer) schedule(ctx context.Context, next *pendingRuntimeProjectionEvent) uint64 {
	if next == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return c.publishLocked(ctx, next)
	}
	if existing, ok := c.pending[next.key]; ok {
		existing.kind = next.kind
		existing.meta = next.meta
		existing.session = next.session
		existing.worktree = next.worktree
		existing.status = next.status
		return 0
	}
	next.timer = time.AfterFunc(c.window, func() {
		c.flush(next.key)
	})
	c.pending[next.key] = next
	return 0
}

func (c *runtimeProjectionEventCoalescer) flush(key runtimeProjectionEventKey) {
	c.mu.Lock()
	pending := c.pending[key]
	if pending == nil {
		c.mu.Unlock()
		return
	}
	delete(c.pending, key)
	c.mu.Unlock()

	c.publish(context.Background(), pending)
}

func (c *runtimeProjectionEventCoalescer) Close() {
	if c == nil {
		return
	}
	var pending []*pendingRuntimeProjectionEvent
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	pending = make([]*pendingRuntimeProjectionEvent, 0, len(c.pending))
	for key, event := range c.pending {
		if event.timer != nil {
			event.timer.Stop()
		}
		pending = append(pending, event)
		delete(c.pending, key)
	}
	c.mu.Unlock()
	for _, event := range pending {
		c.publish(context.Background(), event)
	}
}

func (c *runtimeProjectionEventCoalescer) publish(ctx context.Context, event *pendingRuntimeProjectionEvent) uint64 {
	if c == nil || c.d == nil || event == nil {
		return 0
	}
	projectID := c.d.canonicalProjectID(event.key.projectID)
	rev := c.d.nextRevision(projectID)
	switch event.kind {
	case runtimeProjectionEventSession:
		c.d.publishSessionProjectionEventAtRevision(ctx, projectID, event.meta, event.session, rev)
	case runtimeProjectionEventWorktree:
		c.d.publishWorktreeProjectionEventAtRevision(ctx, projectID, event.key.issueID, event.worktree, rev)
	case runtimeProjectionEventGitStatus:
		c.d.publishGitStatusProjectionEventAtRevision(ctx, projectID, event.key.issueID, event.worktree, event.status, rev)
	}
	return rev
}

func (c *runtimeProjectionEventCoalescer) publishLocked(ctx context.Context, event *pendingRuntimeProjectionEvent) uint64 {
	c.mu.Unlock()
	rev := c.publish(ctx, event)
	c.mu.Lock()
	return rev
}

func runtimeProjectionCoalesceKey(projectID, issueID, fallback string) runtimeProjectionEventKey {
	issueID = strings.TrimSpace(issueID)
	fallback = strings.TrimSpace(fallback)
	if issueID == "" {
		fallback = "fallback:" + fallback
	} else {
		fallback = ""
	}
	return runtimeProjectionEventKey{
		projectID: strings.TrimSpace(projectID),
		issueID:   issueID,
		fallback:  fallback,
	}
}

func cloneGitStatus(status *git.GitStatus) *git.GitStatus {
	if status == nil {
		return nil
	}
	cloned := *status
	cloned.Added = append([]string(nil), status.Added...)
	cloned.Modified = append([]string(nil), status.Modified...)
	cloned.Deleted = append([]string(nil), status.Deleted...)
	cloned.Staged = append([]string(nil), status.Staged...)
	cloned.Untracked = append([]string(nil), status.Untracked...)
	cloned.Conflicted = append([]string(nil), status.Conflicted...)
	return &cloned
}
