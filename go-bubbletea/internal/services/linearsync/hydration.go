package linearsync

import "github.com/riordanpawley/azedarach/internal/domain"

// ReconcileHydratedTasks overlays local runtime state onto a refreshed snapshot
// while preserving the authoritative task payload from the daemon.
func ReconcileHydratedTasks(current, hydrated []domain.Task) []domain.Task {
	if len(hydrated) == 0 {
		return nil
	}

	currentByID := make(map[string]domain.Task, len(current))
	for _, task := range current {
		currentByID[task.ID] = task
	}

	reconciled := make([]domain.Task, 0, len(hydrated))
	for _, task := range hydrated {
		merged := task
		if local, ok := currentByID[task.ID]; ok {
			merged.HasTmuxSession = local.HasTmuxSession
			merged.HasWorktree = local.HasWorktree
			merged.GitAheadCount = local.GitAheadCount
			merged.GitBehindCount = local.GitBehindCount
			merged.HasUncommittedChanges = local.HasUncommittedChanges
			merged.GitAdditions = local.GitAdditions
			merged.GitDeletions = local.GitDeletions
		}
		reconciled = append(reconciled, merged)
	}

	return reconciled
}
