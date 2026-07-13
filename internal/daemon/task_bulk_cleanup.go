package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
)

type taskBulkCleanupRequest = protocol.TaskBulkCleanupRequest
type taskBulkCleanupItem = protocol.TaskBulkCleanupItem
type taskBulkCleanupResult = protocol.TaskBulkCleanupResult

const (
	taskBulkCleanupDefaultPerIssueTimeout = domain.IntegrationCloseTimeout
	taskBulkCleanupMaxPerIssueTimeout     = time.Hour
)

func (d *Daemon) handleTaskBulkCleanup(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var cmd taskBulkCleanupRequest
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	outcome, status, err := daemonTaskCloseOutcomeStatus(cmd.CloseOutcome)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
	}
	if len(cmd.TaskIDs) == 0 && len(cmd.Statuses) == 0 && strings.TrimSpace(cmd.Query) == "" && cmd.UpdatedBefore == nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "at least one issue id or filter is required"), nil
	}
	if strings.TrimSpace(cmd.Query) != "" && len(domain.ContentQueryTerms(cmd.Query)) == 0 {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "candidate query must contain a searchable term"), nil
	}
	if cmd.PerIssueTimeout == 0 {
		cmd.PerIssueTimeout = taskBulkCleanupDefaultPerIssueTimeout
	}
	if cmd.PerIssueTimeout < 0 || cmd.PerIssueTimeout > taskBulkCleanupMaxPerIssueTimeout {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("per_issue_timeout must be greater than zero and no more than %s", taskBulkCleanupMaxPerIssueTimeout)), nil
	}
	for _, raw := range cmd.Statuses {
		switch domain.Status(strings.ToLower(strings.TrimSpace(raw))) {
		case domain.StatusOpen, domain.StatusInProgress, domain.StatusInReview, domain.StatusDone, domain.StatusCancelled:
		default:
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid candidate status: %s", raw)), nil
		}
	}
	projectID := d.projectID(req.Meta)
	client := d.issueClientForProject(projectID)
	if client == nil {
		return d.errorResponse(req, protocol.ErrorCodeUnavailable, "issue store unavailable"), nil
	}
	tasks, err := client.ListWithRuntime(ctx, projectID)
	if err != nil {
		return d.errorResponse(req, projectIssueStoreHealthErrorCode(err), err.Error()), nil
	}
	selected := selectBulkCleanupTasks(tasks, cmd)
	action := string(status)
	result := taskBulkCleanupResult{DryRun: cmd.DryRun, Action: action, Items: make([]taskBulkCleanupItem, 0, len(selected))}
	selectedIDs := make(map[string]bool, len(selected))
	for _, task := range selected {
		selectedIDs[task.ID.String()] = true
		item := taskBulkCleanupItem{TaskID: task.ID.String(), Action: action, Status: string(task.Status)}
		if task.Status == status || task.Status == domain.StatusDone || task.Status == domain.StatusCancelled {
			item.Success, item.Skipped = true, true
			result.Items = append(result.Items, item)
			continue
		}
		if cmd.DryRun {
			item.Success = true
			result.Items = append(result.Items, item)
			continue
		}
		if err := ctx.Err(); err != nil {
			item.Error = err.Error()
			result.Items = append(result.Items, item)
			continue
		}
		itemCtx, cancel := taskBulkCleanupItemContext(ctx, cmd.PerIssueTimeout)
		closeResult, closeErr := d.closeTask(itemCtx, projectID, taskCloseRequest{
			TaskID: task.ID.String(), IntegrateBeforeClose: outcome == domain.IssueCloseCompleted, CloseOutcome: string(outcome),
		}, req)
		cancel()
		if closeErr != nil {
			item.Error = closeErr.Error()
		} else {
			item.Success = true
			item.Result = &closeResult
		}
		result.Items = append(result.Items, item)
	}
	for _, id := range cmd.TaskIDs {
		id = strings.TrimSpace(id)
		if id != "" && !selectedIDs[id] {
			result.Items = append(result.Items, taskBulkCleanupItem{TaskID: id, Action: action, Error: "issue not found"})
		}
	}
	body, err := json.Marshal(result)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	return resp, nil
}

func taskBulkCleanupItemContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, timeout)
}

func selectBulkCleanupTasks(tasks []domain.Task, cmd taskBulkCleanupRequest) []domain.Task {
	ids, statuses := map[string]bool{}, map[string]bool{}
	for _, id := range cmd.TaskIDs {
		ids[strings.TrimSpace(id)] = true
	}
	for _, status := range cmd.Statuses {
		statuses[strings.ToLower(strings.TrimSpace(status))] = true
	}
	query := strings.TrimSpace(cmd.Query)
	explicitTasks := make([]domain.Task, 0, len(ids))
	filteredTasks := make([]domain.Task, 0)
	hasFilters := len(statuses) > 0 || query != "" || cmd.UpdatedBefore != nil
	for _, task := range tasks {
		explicit := ids[task.ID.String()]
		filtered := hasFilters
		if len(statuses) > 0 {
			filtered = filtered && statuses[strings.ToLower(string(task.Status))]
		}
		if query != "" {
			filtered = filtered && domain.TaskMatchesContentQuery(task, query)
		}
		if cmd.UpdatedBefore != nil {
			filtered = filtered && !task.UpdatedAt.After(*cmd.UpdatedBefore)
		}
		if explicit {
			explicitTasks = append(explicitTasks, task)
		} else if filtered {
			filteredTasks = append(filteredTasks, task)
		}
	}
	byID := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		byID[task.ID.String()] = task
	}
	depth := func(task domain.Task) int {
		d := 0
		cur := task
		for cur.ParentID != nil {
			parent, ok := byID[cur.ParentID.String()]
			if !ok {
				break
			}
			d++
			cur = parent
		}
		return d
	}
	less := func(tasks []domain.Task, i, j int) bool {
		di, dj := depth(tasks[i]), depth(tasks[j])
		if di != dj {
			return di > dj
		}
		return tasks[i].ID.String() < tasks[j].ID.String()
	}
	sort.SliceStable(filteredTasks, func(i, j int) bool { return less(filteredTasks, i, j) })
	if cmd.Limit > 0 && len(filteredTasks) > cmd.Limit {
		filteredTasks = filteredTasks[:cmd.Limit]
	}
	selected := append(explicitTasks, filteredTasks...)
	sort.SliceStable(selected, func(i, j int) bool { return less(selected, i, j) })
	return selected
}
