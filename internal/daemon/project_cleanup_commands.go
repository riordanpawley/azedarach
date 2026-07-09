package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

const (
	projectCleanupCategoryDeleteOldDone      = "delete_old_done"
	projectCleanupCategoryArchiveDone        = "archive_done"
	projectCleanupCategoryRemoveOrphaned     = "remove_orphaned_worktrees"
	projectCleanupCategoryCleanStaleSessions = "clean_stale_sessions"
	projectCleanupDoneRetentionDays          = 30
	projectCleanupStaleSessionRetentionHours = 24
)

func (d *Daemon) handleProjectCleanup(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var cmd protocol.ProjectCleanupRequestBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if bodyProjectID := protocol.TrimProjectID(cmd.ProjectID.String()); bodyProjectID != "" {
		projectID = bodyProjectID
	}
	categories := normalizeProjectCleanupCategories(cmd.Categories)
	result := protocol.ProjectCleanupResponseBody{ProjectID: naming.ProjectID(projectID)}
	if len(categories) == 0 {
		body, err := json.Marshal(result)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		resp := d.successResponse(req)
		resp.Body = body
		resp.Revision = d.currentRevision(projectID)
		return resp, nil
	}

	var tasks []domain.Task
	var tasksLoaded bool
	var err error
	for _, category := range categories {
		switch category {
		case projectCleanupCategoryDeleteOldDone:
			tasks, tasksLoaded, err = d.loadCleanupTasks(ctx, projectID, tasks, tasksLoaded)
			if err != nil {
				return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("load cleanup task graph: %v", err)), nil
			}
			deleted, err := d.cleanupOldDoneTasks(ctx, req, projectID, tasks)
			if err != nil && d.cfg.Logger != nil {
				d.cfg.Logger.Warn("daemon project cleanup delete_old_done failed", "project_id", projectID, "error", err)
			}
			result.Deleted += deleted
		case projectCleanupCategoryArchiveDone:
			tasks, tasksLoaded, err = d.loadCleanupTasks(ctx, projectID, tasks, tasksLoaded)
			if err != nil {
				return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("load cleanup task graph: %v", err)), nil
			}
			archived, err := d.cleanupArchiveDoneTasks(ctx, req, projectID, tasks)
			if err != nil && d.cfg.Logger != nil {
				d.cfg.Logger.Warn("daemon project cleanup archive_done failed", "project_id", projectID, "error", err)
			}
			result.Archived += archived
		case projectCleanupCategoryRemoveOrphaned:
			removed, err := d.cleanupOrphanedWorktrees(ctx, projectID)
			if err != nil && d.cfg.Logger != nil {
				d.cfg.Logger.Warn("daemon project cleanup orphaned worktrees failed", "project_id", projectID, "error", err)
			}
			result.WorktreesRemoved += removed
		case projectCleanupCategoryCleanStaleSessions:
			tasks, tasksLoaded, err = d.loadCleanupTasks(ctx, projectID, tasks, tasksLoaded)
			if err != nil {
				return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("load cleanup task graph: %v", err)), nil
			}
			cleaned, err := d.cleanupStaleSessions(ctx, req, projectID, tasks)
			if err != nil && d.cfg.Logger != nil {
				d.cfg.Logger.Warn("daemon project cleanup stale sessions failed", "project_id", projectID, "error", err)
			}
			result.SessionsCleaned += cleaned
		default:
			if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("daemon project cleanup ignored unknown category", "project_id", projectID, "category", category)
			}
		}
	}

	body, err := json.Marshal(result)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	resp.Revision = d.currentRevision(projectID)
	return resp, nil
}

func (d *Daemon) loadCleanupTasks(ctx context.Context, projectID string, tasks []domain.Task, loaded bool) ([]domain.Task, bool, error) {
	if loaded {
		return tasks, true, nil
	}
	tasks, err := d.loadTaskGraphDomainTasks(ctx, projectID)
	if err != nil {
		return nil, false, err
	}
	return tasks, true, nil
}

func normalizeProjectCleanupCategories(categories []string) []string {
	out := make([]string, 0, len(categories))
	seen := make(map[string]struct{}, len(categories))
	for _, category := range categories {
		category = strings.TrimSpace(category)
		if category == "" {
			continue
		}
		if _, ok := seen[category]; ok {
			continue
		}
		seen[category] = struct{}{}
		out = append(out, category)
	}
	return out
}

func (d *Daemon) cleanupOldDoneTasks(ctx context.Context, req protocol.RequestEnvelope, projectID string, tasks []domain.Task) (int, error) {
	cutoff := timeNow().AddDate(0, 0, -projectCleanupDoneRetentionDays)
	deleted := 0
	var lastErr error
	for _, task := range tasks {
		if !task.IssueClosed() || !task.UpdatedAt.Before(cutoff) {
			continue
		}
		if err := d.deleteTaskForCleanup(ctx, req, projectID, task.ID.String()); err != nil {
			lastErr = err
			continue
		}
		deleted++
	}
	return deleted, lastErr
}

func (d *Daemon) cleanupArchiveDoneTasks(ctx context.Context, req protocol.RequestEnvelope, projectID string, tasks []domain.Task) (int, error) {
	return d.cleanupArchiveDoneTasksBefore(ctx, req, projectID, tasks, time.Time{})
}

func (d *Daemon) cleanupArchiveDoneTasksBefore(ctx context.Context, req protocol.RequestEnvelope, projectID string, tasks []domain.Task, cutoff time.Time) (int, error) {
	archived := 0
	var lastErr error
	for _, task := range doneTasksInArchiveOrder(tasks) {
		if !cutoff.IsZero() && !task.UpdatedAt.Before(cutoff) {
			continue
		}
		if err := d.archiveTaskForCleanup(ctx, req, projectID, task.ID.String()); err != nil {
			lastErr = err
			continue
		}
		archived++
	}
	return archived, lastErr
}

func doneTasksInArchiveOrder(tasks []domain.Task) []domain.Task {
	byID := make(map[naming.IssueID]domain.Task, len(tasks))
	done := make([]domain.Task, 0, len(tasks))
	for _, task := range tasks {
		byID[task.ID] = task
		if task.IssueClosed() {
			done = append(done, task)
		}
	}
	depthByID := make(map[naming.IssueID]int, len(done))
	var depth func(naming.IssueID, map[naming.IssueID]struct{}) int
	depth = func(id naming.IssueID, seen map[naming.IssueID]struct{}) int {
		if cached, ok := depthByID[id]; ok {
			return cached
		}
		if _, ok := seen[id]; ok {
			return 0
		}
		task, ok := byID[id]
		if !ok || task.ParentID == nil || task.ParentID.IsZero() {
			depthByID[id] = 0
			return 0
		}
		nextSeen := make(map[naming.IssueID]struct{}, len(seen)+1)
		for seenID := range seen {
			nextSeen[seenID] = struct{}{}
		}
		nextSeen[id] = struct{}{}
		value := 1 + depth(*task.ParentID, nextSeen)
		depthByID[id] = value
		return value
	}
	sort.SliceStable(done, func(i, j int) bool {
		leftDepth := depth(done[i].ID, nil)
		rightDepth := depth(done[j].ID, nil)
		if leftDepth != rightDepth {
			return leftDepth > rightDepth
		}
		return done[i].ID.String() < done[j].ID.String()
	})
	return done
}

func (d *Daemon) cleanupOrphanedWorktrees(ctx context.Context, projectID string) (int, error) {
	if d.worktreeAdapter == nil {
		return 0, fmt.Errorf("worktree cleanup unavailable")
	}
	result, err := d.worktreeAdapter.CleanupOrphaned(ctx, projectID)
	if err != nil {
		return 0, err
	}
	if result == nil {
		return 0, nil
	}
	return len(result.Removed), nil
}

func (d *Daemon) cleanupStaleSessions(ctx context.Context, req protocol.RequestEnvelope, projectID string, tasks []domain.Task) (int, error) {
	cutoff := timeNow().Add(-projectCleanupStaleSessionRetentionHours * time.Hour)
	cleaned := 0
	var lastErr error
	for _, task := range tasks {
		if task.Session == nil || task.Session.StartedAt == nil || !task.Session.StartedAt.Before(cutoff) {
			continue
		}
		if task.Session.State != domain.SessionIdle && task.Session.State != domain.SessionPaused {
			continue
		}
		if err := d.stopSessionForCleanup(ctx, req, projectID, task.ID.String()); err != nil {
			lastErr = err
			continue
		}
		cleaned++
	}
	return cleaned, lastErr
}

func (d *Daemon) deleteTaskForCleanup(ctx context.Context, req protocol.RequestEnvelope, projectID, taskID string) error {
	body, err := json.Marshal(map[string]string{"task_id": taskID})
	if err != nil {
		return err
	}
	cleanupReq := req
	cleanupReq.Command = "task.delete"
	cleanupReq.Body = body
	cleanupReq.Meta.ProjectID = naming.ProjectID(projectID)
	resp, err := d.handleTaskDelete(ctx, cleanupReq)
	return cleanupCommandError(resp, err)
}

func (d *Daemon) archiveTaskForCleanup(ctx context.Context, req protocol.RequestEnvelope, projectID, taskID string) error {
	body, err := json.Marshal(map[string]string{"task_id": taskID})
	if err != nil {
		return err
	}
	cleanupReq := req
	cleanupReq.Command = "task.archive"
	cleanupReq.Body = body
	cleanupReq.Meta.ProjectID = naming.ProjectID(projectID)
	resp, err := d.handleTaskArchive(ctx, cleanupReq)
	return cleanupCommandError(resp, err)
}

func (d *Daemon) stopSessionForCleanup(ctx context.Context, req protocol.RequestEnvelope, projectID, issueID string) error {
	body, err := json.Marshal(sessionCommandBody{
		ProjectID: projectID,
		SessionID: issueID,
	})
	if err != nil {
		return err
	}
	cleanupReq := req
	cleanupReq.Command = "session.stop"
	cleanupReq.Body = body
	cleanupReq.Meta.ProjectID = naming.ProjectID(projectID)
	resp, err := d.handleSessionStopDirect(ctx, cleanupReq)
	return cleanupCommandError(resp, err)
}

func cleanupCommandError(resp protocol.ResponseEnvelope, err error) error {
	if err != nil {
		return err
	}
	if resp.OK {
		return nil
	}
	if resp.Error != nil {
		return errors.New(resp.Error.Message)
	}
	return errors.New("cleanup command failed")
}
