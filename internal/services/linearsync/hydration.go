package linearsync

import (
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

// ReconcileHydratedTasks overlays local runtime state onto a refreshed snapshot
// while preserving the authoritative task payload from the daemon.
func ReconcileHydratedTasks(current, hydrated []domain.Task) []domain.Task {
	if len(hydrated) == 0 {
		return nil
	}

	currentByID := make(map[naming.IssueID]domain.Task, len(current))
	for _, task := range current {
		currentByID[task.ID] = task
	}

	reconciled := make([]domain.Task, 0, len(hydrated))
	for _, task := range hydrated {
		merged := task
		if local, ok := currentByID[task.ID]; ok {
			// Keep prior session projection when a refreshed snapshot temporarily
			// omits session details for an otherwise unchanged task.
			if merged.Session == nil && local.Session != nil {
				merged.Session = cloneSession(local.Session)
			}
			merged.HasWorktree = local.HasWorktree
			merged.GitAheadCount = local.GitAheadCount
			merged.GitBehindCount = local.GitBehindCount
			merged.HasUncommittedChanges = local.HasUncommittedChanges
			merged.GitAdditions = local.GitAdditions
			merged.GitDeletions = local.GitDeletions
		}
		merged.HasTmuxSession = merged.Session != nil
		reconciled = append(reconciled, merged)
	}

	return reconciled
}

func cloneSession(session *domain.Session) *domain.Session {
	if session == nil {
		return nil
	}
	cloned := *session
	if session.StartedAt != nil {
		startedAt := *session.StartedAt
		cloned.StartedAt = &startedAt
	}
	if session.DevServer != nil {
		devServer := *session.DevServer
		cloned.DevServer = &devServer
	}
	return &cloned
}
