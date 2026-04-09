package linearsync

import "github.com/riordanpawley/azedarach/internal/domain"

// ReconcileHydratedTasks returns daemon-hydrated tasks while cloning nested
// runtime pointers for local UI safety.
func ReconcileHydratedTasks(current, hydrated []domain.Task) []domain.Task {
	_ = current
	if len(hydrated) == 0 {
		return nil
	}

	reconciled := make([]domain.Task, 0, len(hydrated))
	for _, task := range hydrated {
		merged := task
		merged.Session = cloneSession(merged.Session)
		merged.HasTmuxSession = merged.Session != nil || merged.HasTmuxSession
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
