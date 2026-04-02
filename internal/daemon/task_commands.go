package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

type taskListReader interface {
	List(context.Context) ([]domain.Task, error)
}

type tmuxSessionReader interface {
	ListSessions(context.Context) ([]string, error)
}

type taskSnapshotExportBody struct {
	SchemaVersion    uint16                      `json:"schema_version"`
	ProtocolVersion  protocol.Version            `json:"protocol_version"`
	SnapshotRevision uint64                      `json:"snapshot_revision"`
	CapturedAtMs     int64                       `json:"captured_at_ms"`
	ProjectID        string                      `json:"project_id"`
	TaskCount        int                         `json:"task_count"`
	SessionCount     int                         `json:"session_count"`
	Tasks            []taskSnapshotExportTask    `json:"tasks"`
	Sessions         []taskSnapshotExportSession `json:"sessions"`
}

type taskSnapshotExportTask struct {
	ID              string          `json:"id"`
	Title           string          `json:"title"`
	Status          domain.Status   `json:"status"`
	Priority        domain.Priority `json:"priority"`
	Type            domain.TaskType `json:"issue_type"`
	ParentID        *string         `json:"parent_id,omitempty"`
	DependencyCount int             `json:"dependency_count"`
	SessionAttached bool            `json:"session_attached"`
	Critical        bool            `json:"critical"`
}

type taskSnapshotExportSession struct {
	Name string `json:"name"`
}

func (d *Daemon) handleTaskList(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	resp := d.successResponse(req)
	projectID := d.projectID(req.Meta)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task list requested", "project_id", projectID)
	}
	if _, err := d.reconcileTmuxAndDaemonSessions(ctx, projectID, ""); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Warn("session reconciliation during task list failed", "project_id", projectID, "error", err)
	}
	d.refreshWorktreeProjectionCache(ctx, projectID)
	tasks, err := d.issues.List(ctx)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	tasks = d.enrichTasksWithSessionState(ctx, projectID, tasks)
	tasks = d.enrichTasksWithRuntimeProjectionCache(ctx, projectID, tasks)
	body, err := json.Marshal(tasks)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp.Body = body
	resp.Revision = d.currentRevision(projectID)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task list completed", "project_id", projectID, "task_count", len(tasks), "revision", resp.Revision)
	}
	return resp, nil
}

func (d *Daemon) refreshWorktreeProjectionCache(ctx context.Context, projectID string) {
	if d == nil || d.worktree == nil || d.projectionStore == nil {
		return
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}

	worktrees, err := d.worktree.List(ctx)
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("refresh worktree projection cache failed", "project_id", projectID, "error", err)
		}
		return
	}

	rows := make([]daemonstate.WorktreeProjection, 0, len(worktrees))
	now := time.Now().UTC()
	for _, wt := range worktrees {
		issueID := strings.TrimSpace(wt.IssueID)
		if issueID == "" {
			continue
		}
		rows = append(rows, daemonstate.WorktreeProjection{
			ProjectID: projectID,
			IssueID:   issueID,
			Path:      strings.TrimSpace(wt.Path),
			Branch:    strings.TrimSpace(wt.Branch),
			UpdatedAt: now,
		})
	}
	if err := d.projectionStore.ReplaceWorktrees(ctx, projectID, rows); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Debug("replace worktree projections during task list failed", "project_id", projectID, "error", err)
	}
}

func (d *Daemon) enrichTasksWithRuntimeProjectionCache(ctx context.Context, projectID string, tasks []domain.Task) []domain.Task {
	if len(tasks) == 0 || d.projectionStore == nil {
		return tasks
	}

	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}

	worktreeRows, err := d.projectionStore.ListWorktrees(ctx, projectID)
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("load worktree projections for task enrichment failed", "project_id", projectID, "error", err)
		}
		return tasks
	}
	if len(worktreeRows) == 0 {
		return tasks
	}

	worktreeByIssue := make(map[string]daemonstate.WorktreeProjection, len(worktreeRows))
	for _, row := range worktreeRows {
		issueID := strings.TrimSpace(row.IssueID)
		if issueID == "" {
			continue
		}
		worktreeByIssue[issueID] = row
	}

	for i := range tasks {
		row, ok := worktreeByIssue[strings.TrimSpace(tasks[i].ID)]
		if !ok {
			continue
		}

		worktreePath := strings.TrimSpace(row.Path)
		tasks[i].HasWorktree = worktreePath != ""
		if tasks[i].Session != nil && tasks[i].Session.Worktree == "" && worktreePath != "" {
			tasks[i].Session.Worktree = worktreePath
		}

		statusRaw := row.GitStatusRaw
		if len(statusRaw) == 0 && worktreePath != "" {
			if status := d.hydrateGitStatusProjection(ctx, projectID, row.IssueID, worktreePath); status != nil {
				tasks[i].HasUncommittedChanges = status.HasChanges
				tasks[i].GitAdditions = len(status.Added) + len(status.Modified) + len(status.Staged)
				tasks[i].GitDeletions = len(status.Deleted)
			}
		}
		if len(statusRaw) == 0 {
			continue
		}

		var status git.GitStatus
		if err := json.Unmarshal(statusRaw, &status); err != nil {
			continue
		}
		tasks[i].HasUncommittedChanges = status.HasChanges
		tasks[i].GitAdditions = len(status.Added) + len(status.Modified) + len(status.Staged)
		tasks[i].GitDeletions = len(status.Deleted)
	}

	return tasks
}

func (d *Daemon) hydrateGitStatusProjection(ctx context.Context, projectID, issueID, worktree string) *git.GitStatus {
	if d == nil || d.git == nil || d.projectionStore == nil {
		return nil
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}
	issueID = strings.TrimSpace(issueID)
	worktree = strings.TrimSpace(worktree)
	if issueID == "" || worktree == "" {
		return nil
	}

	timeoutCtx := ctx
	cancel := func() {}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		timeoutCtx, cancel = context.WithTimeout(ctx, 3*time.Second)
	}
	defer cancel()

	status, err := d.git.Status(timeoutCtx, worktree)
	if err != nil || status == nil {
		return nil
	}
	rawStatus, err := json.Marshal(status)
	if err != nil {
		return nil
	}
	if err := d.projectionStore.UpsertWorktreeGitStatus(timeoutCtx, projectID, issueID, rawStatus, time.Now().UTC()); err != nil {
		return nil
	}
	d.publishGitStatusProjectionEvent(timeoutCtx, projectID, issueID, worktree, status)
	return status
}

func (d *Daemon) handleTaskCreate(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	resp := d.successResponse(req)
	projectID := d.projectID(req.Meta)
	var cmd struct {
		Title           string          `json:"title"`
		Description     string          `json:"description"`
		Type            domain.TaskType `json:"type"`
		Priority        domain.Priority `json:"priority"`
		Status          domain.Status   `json:"status,omitempty"`
		Assignee        string          `json:"assignee,omitempty"`
		Labels          []string        `json:"labels,omitempty"`
		Implementations []string        `json:"implementations,omitempty"`
		Design          string          `json:"design,omitempty"`
		Notes           string          `json:"notes,omitempty"`
		Acceptance      string          `json:"acceptance,omitempty"`
		Estimate        *int            `json:"estimate,omitempty"`
		ParentID        *string         `json:"parent_id,omitempty"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task create requested",
			"project_id", projectID,
			"title", cmd.Title,
			"type", cmd.Type,
			"priority", cmd.Priority,
			"parent_id", cmd.ParentID,
		)
	}
	taskID, err := d.issues.Create(ctx, issues.CreateTaskParams{
		Title:           cmd.Title,
		Description:     cmd.Description,
		Type:            cmd.Type,
		Priority:        cmd.Priority,
		Status:          cmd.Status,
		Assignee:        cmd.Assignee,
		Labels:          cmd.Labels,
		Implementations: cmd.Implementations,
		Design:          cmd.Design,
		Notes:           cmd.Notes,
		Acceptance:      cmd.Acceptance,
		Estimate:        cmd.Estimate,
		ParentID:        cmd.ParentID,
	})
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	body, _ := json.Marshal(struct {
		TaskID string `json:"task_id"`
	}{TaskID: taskID})
	resp.Body = body
	resp.Revision = d.nextRevision(projectID)
	d.publishTaskEvent(req, "task.created", resp.Revision)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task create completed", "project_id", projectID, "task_id", taskID, "revision", resp.Revision)
	}
	return resp, nil
}

func (d *Daemon) handleTaskUpdateStatus(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var cmd struct {
		TaskID string        `json:"task_id"`
		Status domain.Status `json:"status"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task status update requested", "project_id", projectID, "task_id", cmd.TaskID, "status", cmd.Status)
	}
	if err := d.issues.Update(ctx, cmd.TaskID, cmd.Status); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Revision = d.nextRevision(projectID)
	d.publishTaskEvent(req, "task.updated", resp.Revision)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task status update completed", "project_id", projectID, "task_id", cmd.TaskID, "status", cmd.Status, "revision", resp.Revision)
	}
	return resp, nil
}

func (d *Daemon) handleTaskUpdateDetails(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var cmd struct {
		TaskID          string          `json:"task_id"`
		Title           string          `json:"title"`
		Description     string          `json:"description"`
		Type            domain.TaskType `json:"type"`
		Priority        domain.Priority `json:"priority"`
		Implementations []string        `json:"implementations,omitempty"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task details update requested", "project_id", projectID, "task_id", cmd.TaskID)
	}
	if err := d.issues.UpdateDetails(ctx, cmd.TaskID, issues.UpdateTaskParams{
		Title:           cmd.Title,
		Description:     cmd.Description,
		Type:            cmd.Type,
		Priority:        cmd.Priority,
		Implementations: cmd.Implementations,
	}); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Revision = d.nextRevision(projectID)
	d.publishTaskEvent(req, "task.updated", resp.Revision)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task details update completed", "project_id", projectID, "task_id", cmd.TaskID, "revision", resp.Revision)
	}
	return resp, nil
}

func (d *Daemon) handleTaskAppendNotes(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var cmd struct {
		TaskID string `json:"task_id"`
		Line   string `json:"line"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task append notes requested", "project_id", projectID, "task_id", cmd.TaskID, "line_bytes", len(cmd.Line))
	}
	if err := d.issues.AppendNotes(ctx, cmd.TaskID, cmd.Line); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Revision = d.nextRevision(projectID)
	d.publishTaskEvent(req, "task.updated", resp.Revision)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task append notes completed", "project_id", projectID, "task_id", cmd.TaskID, "revision", resp.Revision)
	}
	return resp, nil
}

func (d *Daemon) handleTaskDelete(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var cmd struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task delete requested", "project_id", projectID, "task_id", cmd.TaskID)
	}
	if err := d.issues.Delete(ctx, cmd.TaskID); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Revision = d.nextRevision(projectID)
	d.publishTaskEvent(req, "task.deleted", resp.Revision)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task delete completed", "project_id", projectID, "task_id", cmd.TaskID, "revision", resp.Revision)
	}
	return resp, nil
}

func (d *Daemon) handleTaskArchive(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var cmd struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task archive requested", "project_id", projectID, "task_id", cmd.TaskID)
	}
	if err := d.issues.Archive(ctx, cmd.TaskID); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Revision = d.nextRevision(projectID)
	d.publishTaskEvent(req, "task.archived", resp.Revision)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task archive completed", "project_id", projectID, "task_id", cmd.TaskID, "revision", resp.Revision)
	}
	return resp, nil
}

func (d *Daemon) handleTaskDependencyAdd(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var cmd struct {
		TaskID         string `json:"task_id"`
		DependsOnID    string `json:"depends_on_id"`
		DependencyType string `json:"dependency_type"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task dependency add requested",
			"project_id", projectID,
			"task_id", cmd.TaskID,
			"depends_on_id", cmd.DependsOnID,
			"dependency_type", cmd.DependencyType,
		)
	}
	if err := d.issues.AddDependency(ctx, cmd.TaskID, cmd.DependsOnID, cmd.DependencyType); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Revision = d.nextRevision(projectID)
	d.publishTaskEvent(req, "task.updated", resp.Revision)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task dependency add completed",
			"project_id", projectID,
			"task_id", cmd.TaskID,
			"depends_on_id", cmd.DependsOnID,
			"dependency_type", cmd.DependencyType,
			"revision", resp.Revision,
		)
	}
	return resp, nil
}

func (d *Daemon) handleTaskDependencyRemove(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var cmd struct {
		TaskID         string `json:"task_id"`
		DependsOnID    string `json:"depends_on_id"`
		DependencyType string `json:"dependency_type"`
		Confirm        bool   `json:"confirm"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task dependency remove requested",
			"project_id", projectID,
			"task_id", cmd.TaskID,
			"depends_on_id", cmd.DependsOnID,
			"dependency_type", cmd.DependencyType,
			"confirm", cmd.Confirm,
		)
	}
	callCtx := ctx
	if cmd.Confirm {
		callCtx = issues.WithDependencyRemovalConfirmation(callCtx)
	}
	if err := d.issues.RemoveDependency(callCtx, cmd.TaskID, cmd.DependsOnID, cmd.DependencyType); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Revision = d.nextRevision(projectID)
	d.publishTaskEvent(req, "task.updated", resp.Revision)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task dependency remove completed",
			"project_id", projectID,
			"task_id", cmd.TaskID,
			"depends_on_id", cmd.DependsOnID,
			"dependency_type", cmd.DependencyType,
			"revision", resp.Revision,
		)
	}
	return resp, nil
}

func (d *Daemon) handleTaskSnapshotExport(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task snapshot export requested", "project_id", projectID)
	}
	tasks, err := d.issues.List(ctx)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	sessions, err := d.tmux.ListSessions(ctx)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}

	body := buildTaskSnapshotExportBody(projectID, d.currentRevision(projectID), tasks, sessions, d.sessionNamingScope(projectID))
	payload, err := json.Marshal(body)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal snapshot export body: %v", err)), nil
	}

	resp := d.successResponse(req)
	resp.Revision = body.SnapshotRevision
	resp.Body = payload
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task snapshot export completed",
			"project_id", projectID,
			"task_count", body.TaskCount,
			"session_count", body.SessionCount,
			"snapshot_revision", body.SnapshotRevision,
		)
	}
	return resp, nil
}

func buildTaskSnapshotExportBody(projectID string, revision uint64, tasks []domain.Task, tmuxSessions []string, projectPath string) taskSnapshotExportBody {
	taskCopy := make([]domain.Task, len(tasks))
	copy(taskCopy, tasks)
	sort.SliceStable(taskCopy, func(i, j int) bool {
		return taskCopy[i].ID < taskCopy[j].ID
	})

	sessionCopy := make([]string, len(tmuxSessions))
	copy(sessionCopy, tmuxSessions)
	sort.Strings(sessionCopy)

	sessionSet := make(map[string]struct{}, len(sessionCopy))
	for _, session := range sessionCopy {
		sessionSet[sessionKey(session)] = struct{}{}
		if issueID, ok := naming.ParseIssueIDFromSessionName(session, projectPath); ok {
			sessionSet[sessionKey(issueID)] = struct{}{}
		}
	}

	out := taskSnapshotExportBody{
		SchemaVersion:    protocol.SnapshotSchemaVersion,
		ProtocolVersion:  protocol.SnapshotProtocolVersion,
		SnapshotRevision: revision,
		CapturedAtMs:     nowUnixMs(),
		ProjectID:        projectID,
		TaskCount:        len(taskCopy),
		SessionCount:     len(sessionCopy),
		Tasks:            make([]taskSnapshotExportTask, 0, len(taskCopy)),
		Sessions:         make([]taskSnapshotExportSession, 0, len(sessionCopy)),
	}

	for _, task := range taskCopy {
		_, hasSession := sessionSet[sessionKey(task.ID)]
		out.Tasks = append(out.Tasks, taskSnapshotExportTask{
			ID:              task.ID,
			Title:           task.Title,
			Status:          task.Status,
			Priority:        task.Priority,
			Type:            task.Type,
			ParentID:        task.ParentID,
			DependencyCount: len(task.Dependencies),
			SessionAttached: hasSession,
			Critical:        task.Status == domain.StatusBlocked,
		})
	}

	for _, session := range sessionCopy {
		out.Sessions = append(out.Sessions, taskSnapshotExportSession{Name: session})
	}

	return out
}

var nowUnixMs = func() int64 {
	return timeNow().UnixMilli()
}

var timeNow = func() time.Time {
	return time.Now().UTC()
}
