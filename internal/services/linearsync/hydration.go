package linearsync

import "github.com/riordanpawley/azedarach/internal/domain"

// ReconcileHydratedTasks returns daemon-hydrated tasks while cloning nested
// runtime pointers for local UI safety. Summary snapshots intentionally omit
// long detail fields, so preserve any full details already loaded in the UI.
func ReconcileHydratedTasks(current, hydrated []domain.Task) []domain.Task {
	if len(hydrated) == 0 {
		return nil
	}

	currentByID := make(map[string]domain.Task, len(current))
	for _, task := range current {
		currentByID[task.ID.String()] = task
	}

	reconciled := make([]domain.Task, 0, len(hydrated))
	for _, task := range hydrated {
		merged := task
		if existing, ok := currentByID[task.ID.String()]; ok {
			preserveSummaryOmittedDetails(&merged, existing)
		}
		merged.Session = cloneSession(merged.Session)
		merged.HasTmuxSession = merged.Session != nil || merged.HasTmuxSession
		reconciled = append(reconciled, merged)
	}

	return reconciled
}

func preserveSummaryOmittedDetails(task *domain.Task, existing domain.Task) {
	if task.Description == "" {
		task.Description = existing.Description
	}
	if task.Notes == "" {
		task.Notes = existing.Notes
	}
	if task.Design == "" {
		task.Design = existing.Design
	}
	if task.Acceptance == "" {
		task.Acceptance = existing.Acceptance
	}
	if task.Estimate == nil {
		task.Estimate = cloneInt(existing.Estimate)
	}
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
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
