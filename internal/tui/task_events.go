package app

import (
	"encoding/json"
	"strings"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
)

func isTaskMutationEvent(event string) bool {
	switch event {
	case protocol.EventTaskCreated,
		protocol.EventTaskUpdated,
		protocol.EventTaskDeleted,
		protocol.EventTaskArchived:
		return true
	default:
		return false
	}
}

func (m *Model) applyTaskEvent(evt protocol.EventEnvelope) bool {
	if !isTaskMutationEvent(evt.Event) || len(evt.Body) == 0 {
		return false
	}

	var body protocol.TaskEventBody
	if err := json.Unmarshal(evt.Body, &body); err != nil {
		if m.logger != nil {
			m.logger.Warn("decode task event failed", "event", evt.Event, "revision", evt.Revision, "error", err)
		}
		return false
	}
	if projectID := strings.TrimSpace(body.ProjectID.String()); projectID != "" && projectID != m.daemonProjectID() {
		return false
	}

	taskID := strings.TrimSpace(body.TaskID.String())
	if taskID == "" && body.Task != nil {
		taskID = strings.TrimSpace(body.Task.ID.String())
	}
	if taskID == "" {
		return false
	}

	switch evt.Event {
	case protocol.EventTaskDeleted, protocol.EventTaskArchived:
		m.tasks = removeTaskByID(m.tasks, taskID)
	case protocol.EventTaskCreated, protocol.EventTaskUpdated:
		if body.Task == nil {
			return false
		}
		m.upsertTaskFromEvent(*body.Task)
	default:
		return false
	}

	m.syncProjectionIndexesFromTasks()
	m.applyPendingStatusOverlays()
	m.reconcilePendingStatuses()
	m.reconcilePendingOperations()
	m.editor.ReconcileSelection(m.tasks)
	m.applyPendingCreatedTaskSelection()
	m.reconcileCursorAfterIssuesRefresh()
	m.syncTaskWorkspaceOverlay()
	return true
}

func (m *Model) upsertTaskFromEvent(task domain.Task) {
	taskKey := taskIDKey(task.ID.String())
	for i := range m.tasks {
		if taskIDKey(m.tasks[i].ID.String()) != taskKey {
			continue
		}
		m.tasks[i] = task
		m.tasks[i].Session = cloneSession(task.Session)
		return
	}
	task.Session = cloneSession(task.Session)
	m.tasks = append(m.tasks, task)
}
