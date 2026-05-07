package app

import (
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/ui/board"
)

func (m *Model) syncProjectionIndexesFromTasks() {
	nextSessions := make(map[string]*domain.Session, len(m.tasks))
	nextSignals := make(map[string]board.RuntimeSignals, len(m.tasks))
	nextWorktrees := make(map[string]string, len(m.tasks))

	for i := range m.tasks {
		task := &m.tasks[i]
		taskID := strings.TrimSpace(task.ID.String())
		if taskID == "" {
			continue
		}
		if task.Session != nil {
			nextSessions[taskID] = cloneSession(task.Session)
		}

		signals := m.runtimeSignalsByTask[taskID]
		signals.HasTmuxSession = signals.HasTmuxSession || task.Session != nil || task.HasTmuxSession
		signals.HasWorktree = signals.HasWorktree || task.HasWorktree
		signals.GitAheadCount = task.GitAheadCount
		signals.GitBehindCount = task.GitBehindCount
		signals.HasUncommittedChanges = task.HasUncommittedChanges
		signals.GitAdditions = task.GitAdditions
		signals.GitDeletions = task.GitDeletions
		signals.PendingOperationID = ""
		signals.PendingOperationState = ""
		signals.PendingOperationPercent = 0
		nextSignals[taskID] = signals

		if task.Session != nil {
			if worktreePath := strings.TrimSpace(task.Session.Worktree); worktreePath != "" {
				nextWorktrees[taskID] = worktreePath
			} else if cached := strings.TrimSpace(m.runtimeSignalWorktreeByTask[taskID]); cached != "" {
				nextWorktrees[taskID] = cached
			}
		} else if cached := strings.TrimSpace(m.runtimeSignalWorktreeByTask[taskID]); cached != "" {
			nextWorktrees[taskID] = cached
		}
	}

	m.sessions = nextSessions
	m.runtimeSignalsByTask = nextSignals
	m.runtimeSignalWorktreeByTask = nextWorktrees
}

func (m *Model) applyRuntimeProjection(projection protocol.RuntimeProjection) bool {
	issueID := strings.TrimSpace(projection.IssueID.String())
	if issueID == "" {
		return false
	}

	for i := range m.tasks {
		if taskIDKey(m.tasks[i].ID.String()) != taskIDKey(issueID) {
			continue
		}

		task := &m.tasks[i]
		task.HasWorktree = projection.Worktree.Exists
		task.GitAheadCount = projection.Git.GitAheadCount
		task.GitBehindCount = projection.Git.GitBehindCount
		task.HasUncommittedChanges = projection.Git.HasUncommittedChanges
		task.GitAdditions = projection.Git.GitAdditions
		task.GitDeletions = projection.Git.GitDeletions

		taskID := task.ID.String()
		signals := m.runtimeSignalsByTask[taskID]
		signals.HasTmuxSession = projection.Session.HasSession
		signals.HasWorktree = projection.Worktree.Exists
		signals.GitAheadCount = projection.Git.GitAheadCount
		signals.GitBehindCount = projection.Git.GitBehindCount
		signals.HasUncommittedChanges = projection.Git.HasUncommittedChanges
		signals.GitAdditions = projection.Git.GitAdditions
		signals.GitDeletions = projection.Git.GitDeletions
		if op := projection.Git.ActiveOperation; op != nil {
			signals.PendingOperationID = strings.TrimSpace(op.OperationID.String())
			signals.PendingOperationState = string(op.State)
			signals.PendingOperationPercent = op.ProgressPercent
		} else {
			signals.PendingOperationID = ""
			signals.PendingOperationState = ""
			signals.PendingOperationPercent = 0
		}
		m.runtimeSignalsByTask[taskID] = signals

		if worktreePath := strings.TrimSpace(projection.Session.Worktree); worktreePath != "" {
			m.runtimeSignalWorktreeByTask[taskID] = worktreePath
		} else if projection.Worktree.Exists && strings.TrimSpace(projection.Worktree.Path) != "" {
			m.runtimeSignalWorktreeByTask[taskID] = strings.TrimSpace(projection.Worktree.Path)
		} else {
			delete(m.runtimeSignalWorktreeByTask, taskID)
		}

		if projection.Session.HasSession {
			next := cloneSession(task.Session)
			if next == nil {
				next = &domain.Session{}
			}
			next.IssueID = task.ID
			if state, ok := projectSessionLifecycleState(projection.Session.State); ok {
				next.State = state
			} else {
				task.Session = nil
				task.HasTmuxSession = false
				delete(m.sessions, taskID)
				m.syncTaskWorkspaceOverlay()
				return true
			}
			if worktreePath := strings.TrimSpace(projection.Session.Worktree); worktreePath != "" {
				next.Worktree = worktreePath
			} else if projection.Worktree.Exists {
				next.Worktree = strings.TrimSpace(projection.Worktree.Path)
			}
			startedAt := projection.Session.StartedAt
			if startedAt == nil || startedAt.IsZero() {
				startedAt = projection.Session.UpdatedAt
			}
			if startedAt != nil && !startedAt.IsZero() {
				utc := startedAt.UTC()
				next.StartedAt = &utc
			} else {
				next.StartedAt = nil
			}
			task.Session = next
			task.HasTmuxSession = true
			m.sessions[taskID] = cloneSession(next)
		} else {
			task.Session = nil
			task.HasTmuxSession = false
			delete(m.sessions, taskID)
		}

		m.syncTaskWorkspaceOverlay()
		return true
	}

	return false
}

func (m *Model) applyRuntimeProjectionFromSessionEvent(body protocol.SessionProjectionEventBody) bool {
	updated := false
	if body.Runtime != nil {
		updated = m.applyRuntimeProjection(body.Runtime.Projection) || updated
	}

	issueID := strings.TrimSpace(body.Session.IssueID.String())
	if issueID == "" {
		return updated
	}

	for i := range m.tasks {
		if taskIDKey(m.tasks[i].ID.String()) != taskIDKey(issueID) {
			continue
		}

		nextState, hasSession := projectSessionLifecycleState(body.Session.State)
		if !hasSession {
			m.tasks[i].Session = nil
			m.tasks[i].HasTmuxSession = false
			delete(m.sessions, m.tasks[i].ID.String())
			m.syncTaskWorkspaceOverlay()
			return true
		}

		next := cloneSession(m.tasks[i].Session)
		if next == nil {
			next = &domain.Session{IssueID: naming.IssueID(issueID)}
		}
		next.IssueID = naming.IssueID(issueID)
		next.State = nextState
		if next.StartedAt == nil {
			startedAt := body.Session.UpdatedAt
			next.StartedAt = &startedAt
		}
		if body.Runtime != nil {
			if worktreePath := strings.TrimSpace(body.Runtime.Projection.Session.Worktree); worktreePath != "" {
				next.Worktree = worktreePath
			}
		}
		m.tasks[i].Session = next
		m.tasks[i].HasTmuxSession = true
		m.sessions[m.tasks[i].ID.String()] = cloneSession(next)
		m.syncTaskWorkspaceOverlay()
		return true
	}

	return updated
}

func (m *Model) applyRuntimeProjectionFromUpdateEvent(body protocol.ProjectionUpdateEventBody) bool {
	if body.Runtime == nil {
		return false
	}
	return m.applyRuntimeProjection(body.Runtime.Projection)
}

func runtimeProjectionStartedAt(startedAt *time.Time, updatedAt *time.Time) *time.Time {
	if startedAt != nil && !startedAt.IsZero() {
		utc := startedAt.UTC()
		return &utc
	}
	if updatedAt != nil && !updatedAt.IsZero() {
		utc := updatedAt.UTC()
		return &utc
	}
	return nil
}
