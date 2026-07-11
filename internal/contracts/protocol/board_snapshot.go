package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

const (
	CommandBoardFetch          = "board.fetch"
	CommandBoardViewList       = "board.view.list"
	CommandBoardViewGet        = "board.view.get"
	CommandBoardViewSave       = "board.view.save"
	CommandBoardViewDelete     = "board.view.delete"
	CommandBoardViewSelect     = "board.view.select"
	BoardSnapshotSchemaVersion = 5
)

// BoardSnapshotPayload is the daemon/client contract for board view snapshots.
//
// Board snapshots are intentionally not domain.Task payloads. They carry the
// fields needed to render and route board actions, but omit long detail fields
// such as description, notes, design, and acceptance.
type BoardSnapshotPayload struct {
	SchemaVersion    uint16              `json:"schema_version" msgpack:"schema_version"`
	ProtocolVersion  Version             `json:"protocol_version" msgpack:"protocol_version"`
	SnapshotRevision uint64              `json:"snapshot_revision" msgpack:"snapshot_revision"`
	ProjectID        naming.ProjectID    `json:"project_id" msgpack:"project_id"`
	LastCheckedAt    time.Time           `json:"last_checked_at" msgpack:"last_checked_at"`
	Freshness        TaskListFreshness   `json:"freshness" msgpack:"freshness"`
	Projection       BoardViewProjection `json:"projection" msgpack:"projection"`
}

type BoardViewProjection struct {
	View         domain.BoardView          `json:"view" msgpack:"view"`
	Groups       []BoardViewProjectedGroup `json:"groups" msgpack:"groups"`
	Items        []BoardViewProjectedItem  `json:"items" msgpack:"items"`
	KnownTaskIDs []naming.IssueID          `json:"known_task_ids,omitempty" msgpack:"known_task_ids,omitempty"`
}

type BoardViewProjectedGroup struct {
	GroupID domain.BoardColumnID `json:"group_id" msgpack:"group_id"`
	TaskIDs []naming.IssueID     `json:"task_ids" msgpack:"task_ids"`
}

type BoardViewProjectedItem struct {
	Task               BoardTaskSummary              `json:"task" msgpack:"task"`
	GroupID            domain.BoardColumnID          `json:"group_id" msgpack:"group_id"`
	Depth              int                           `json:"depth,omitempty" msgpack:"depth,omitempty"`
	OrchestrationState domain.OrchestrationViewState `json:"orchestration_state,omitempty" msgpack:"orchestration_state,omitempty"`
}

func BoardViewProjectionFromDomain(projection domain.BoardViewProjection) BoardViewProjection {
	out := BoardViewProjection{
		View:         projection.View,
		KnownTaskIDs: append([]naming.IssueID(nil), projection.KnownTaskIDs...),
	}
	for _, group := range projection.Groups {
		out.Groups = append(out.Groups, BoardViewProjectedGroup{GroupID: group.GroupID, TaskIDs: append([]naming.IssueID(nil), group.TaskIDs...)})
	}
	for _, item := range projection.Items {
		out.Items = append(out.Items, BoardViewProjectedItem{Task: BoardTaskSummaryFromDomain(item.Task), GroupID: item.GroupID, Depth: item.Depth, OrchestrationState: item.OrchestrationState})
	}
	return out
}

func (p BoardViewProjection) TaskSummaries() []BoardTaskSummary {
	out := make([]BoardTaskSummary, 0, len(p.Items))
	for _, item := range p.Items {
		out = append(out, item.Task)
	}
	return out
}

func (p BoardViewProjection) ColumnSnapshots() []BoardSnapshotColumn {
	items := make(map[string]BoardTaskSummary, len(p.Items))
	for _, item := range p.Items {
		items[item.Task.ID.String()] = item.Task
	}
	out := make([]BoardSnapshotColumn, 0, len(p.Groups))
	definitions := make(map[domain.BoardColumnID]BoardColumn, len(p.View.Columns))
	for _, definition := range p.View.Columns {
		definitions[definition.ID] = definition
	}
	for _, group := range p.Groups {
		column := BoardSnapshotColumn{Definition: definitions[group.GroupID]}
		for _, taskID := range group.TaskIDs {
			if task, ok := items[taskID.String()]; ok {
				column.Tasks = append(column.Tasks, task)
			}
		}
		out = append(out, column)
	}
	return out
}

type BoardSnapshotRequestBody struct {
	ProjectID naming.ProjectID `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	ViewID    string           `json:"view_id,omitempty" msgpack:"view_id,omitempty"`
}

type BoardSnapshotColumn struct {
	Definition BoardColumn        `json:"definition" msgpack:"definition"`
	Tasks      []BoardTaskSummary `json:"tasks" msgpack:"tasks"`
}

type BoardColumn = domain.BoardColumn

type BoardViewListRequestBody struct {
	ProjectID naming.ProjectID `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
}

type BoardViewGetRequestBody struct {
	ProjectID naming.ProjectID `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	ViewID    string           `json:"view_id" msgpack:"view_id"`
}

type BoardViewSaveRequestBody struct {
	ProjectID naming.ProjectID `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	View      domain.BoardView `json:"view" msgpack:"view"`
}

type BoardViewDeleteRequestBody struct {
	ProjectID naming.ProjectID `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	ViewID    string           `json:"view_id" msgpack:"view_id"`
}

type BoardViewSelectRequestBody struct {
	ProjectID naming.ProjectID `json:"project_id,omitempty" msgpack:"project_id,omitempty"`
	ViewID    string           `json:"view_id" msgpack:"view_id"`
}

type BoardViewListResponseBody struct {
	ProjectID      naming.ProjectID         `json:"project_id" msgpack:"project_id"`
	SelectedViewID string                   `json:"selected_view_id,omitempty" msgpack:"selected_view_id,omitempty"`
	Views          []domain.BoardViewRecord `json:"views" msgpack:"views"`
}

type BoardViewResponseBody struct {
	ProjectID naming.ProjectID       `json:"project_id" msgpack:"project_id"`
	View      domain.BoardViewRecord `json:"view" msgpack:"view"`
}

type BoardViewSelectResponseBody struct {
	ProjectID naming.ProjectID `json:"project_id" msgpack:"project_id"`
	ViewID    string           `json:"view_id" msgpack:"view_id"`
	UpdatedAt time.Time        `json:"updated_at" msgpack:"updated_at"`
}

type BoardTaskSummary struct {
	ID                    naming.IssueID         `json:"id" msgpack:"id"`
	Title                 string                 `json:"title" msgpack:"title"`
	Assignee              string                 `json:"assignee,omitempty" msgpack:"assignee,omitempty"`
	Labels                []string               `json:"labels,omitempty" msgpack:"labels,omitempty"`
	Estimate              *int                   `json:"estimate,omitempty" msgpack:"estimate,omitempty"`
	Status                domain.Status          `json:"status" msgpack:"status"`
	State                 domain.IssueState      `json:"issue_state,omitzero" msgpack:"issue_state,omitempty"`
	Facts                 domain.IssueFacts      `json:"issue_facts,omitzero" msgpack:"issue_facts,omitempty"`
	Priority              domain.Priority        `json:"priority" msgpack:"priority"`
	Type                  domain.TaskType        `json:"issue_type" msgpack:"issue_type"`
	ParentID              *naming.IssueID        `json:"parent_id,omitempty" msgpack:"parent_id,omitempty"`
	Dependencies          []domain.Dependency    `json:"dependencies,omitempty" msgpack:"dependencies,omitempty"`
	Implementations       []string               `json:"implementations,omitempty" msgpack:"implementations,omitempty"`
	Session               *domain.Session        `json:"session,omitempty" msgpack:"session,omitempty"`
	HasTmuxSession        bool                   `json:"has_tmux_session,omitempty" msgpack:"has_tmux_session,omitempty"`
	HasWorktree           bool                   `json:"has_worktree,omitempty" msgpack:"has_worktree,omitempty"`
	GitAheadCount         int                    `json:"git_ahead_count,omitempty" msgpack:"git_ahead_count,omitempty"`
	GitBehindCount        int                    `json:"git_behind_count,omitempty" msgpack:"git_behind_count,omitempty"`
	HasUncommittedChanges bool                   `json:"has_uncommitted_changes,omitempty" msgpack:"has_uncommitted_changes,omitempty"`
	HasConflicts          bool                   `json:"has_conflicts,omitempty" msgpack:"has_conflicts,omitempty"`
	ConflictFiles         []string               `json:"conflict_files,omitempty" msgpack:"conflict_files,omitempty"`
	GitAdditions          int                    `json:"git_additions,omitempty" msgpack:"git_additions,omitempty"`
	GitDeletions          int                    `json:"git_deletions,omitempty" msgpack:"git_deletions,omitempty"`
	Origin                string                 `json:"origin,omitempty" msgpack:"origin,omitempty"`
	PullRequest           *domain.PullRequest    `json:"pull_request,omitempty" msgpack:"pull_request,omitempty"`
	Ownership             *domain.IssueOwnership `json:"ownership,omitempty" msgpack:"ownership,omitempty"`
	CreatedAt             time.Time              `json:"created_at" msgpack:"created_at"`
	UpdatedAt             time.Time              `json:"updated_at" msgpack:"updated_at"`
}

func BoardTaskSummaryFromDomain(task domain.Task) BoardTaskSummary {
	facts := task.IssueFacts()
	return BoardTaskSummary{
		ID:                    task.ID,
		Title:                 task.Title,
		Assignee:              task.Assignee,
		Labels:                append([]string(nil), task.Labels...),
		Estimate:              cloneIntPointer(task.Estimate),
		Status:                domain.BoardStatusForTask(task),
		State:                 task.State,
		Facts:                 facts,
		Priority:              task.Priority,
		Type:                  task.Type,
		ParentID:              cloneIssueIDPointer(task.ParentID),
		Dependencies:          append([]domain.Dependency(nil), task.Dependencies...),
		Implementations:       append([]string(nil), task.Implementations...),
		Session:               cloneProtocolSession(task.Session),
		HasTmuxSession:        task.HasTmuxSession,
		HasWorktree:           task.HasWorktree,
		GitAheadCount:         task.GitAheadCount,
		GitBehindCount:        task.GitBehindCount,
		HasUncommittedChanges: task.HasUncommittedChanges,
		HasConflicts:          task.HasConflicts,
		ConflictFiles:         append([]string(nil), task.ConflictFiles...),
		GitAdditions:          task.GitAdditions,
		GitDeletions:          task.GitDeletions,
		Origin:                task.Origin,
		PullRequest:           cloneProtocolPullRequest(task.PullRequest),
		Ownership:             cloneProtocolOwnership(task.Ownership),
		CreatedAt:             task.CreatedAt,
		UpdatedAt:             task.UpdatedAt,
	}
}

func (s BoardTaskSummary) ToDomainTask() domain.Task {
	return domain.Task{
		ID:                    s.ID,
		Title:                 s.Title,
		Assignee:              s.Assignee,
		Labels:                append([]string(nil), s.Labels...),
		Estimate:              cloneIntPointer(s.Estimate),
		Status:                s.Status,
		State:                 s.State,
		Facts:                 s.Facts,
		Priority:              s.Priority,
		Type:                  s.Type,
		ParentID:              cloneIssueIDPointer(s.ParentID),
		Dependencies:          append([]domain.Dependency(nil), s.Dependencies...),
		Implementations:       append([]string(nil), s.Implementations...),
		Session:               cloneProtocolSession(s.Session),
		HasTmuxSession:        s.HasTmuxSession,
		HasWorktree:           s.HasWorktree,
		GitAheadCount:         s.GitAheadCount,
		GitBehindCount:        s.GitBehindCount,
		HasUncommittedChanges: s.HasUncommittedChanges,
		HasConflicts:          s.HasConflicts,
		ConflictFiles:         append([]string(nil), s.ConflictFiles...),
		GitAdditions:          s.GitAdditions,
		GitDeletions:          s.GitDeletions,
		Origin:                s.Origin,
		PullRequest:           cloneProtocolPullRequest(s.PullRequest),
		Ownership:             cloneProtocolOwnership(s.Ownership),
		CreatedAt:             s.CreatedAt,
		UpdatedAt:             s.UpdatedAt,
	}
}

func BoardTaskSummariesFromDomain(tasks []domain.Task) []BoardTaskSummary {
	if len(tasks) == 0 {
		return nil
	}
	out := make([]BoardTaskSummary, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, BoardTaskSummaryFromDomain(task))
	}
	return out
}

func DomainTasksFromBoardSummaries(tasks []BoardTaskSummary) []domain.Task {
	if len(tasks) == 0 {
		return nil
	}
	out := make([]domain.Task, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, task.ToDomainTask())
	}
	return out
}

func cloneProtocolPullRequest(pr *domain.PullRequest) *domain.PullRequest {
	if pr == nil {
		return nil
	}
	cloned := *pr
	return &cloned
}

func DecodeBoardSnapshotPayload(data []byte) (BoardSnapshotPayload, error) {
	var payload BoardSnapshotPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return BoardSnapshotPayload{}, err
	}
	if payload.SchemaVersion != BoardSnapshotSchemaVersion {
		return BoardSnapshotPayload{}, &TaskListSnapshotVersionMismatchError{
			Field:    "schema_version",
			Expected: BoardSnapshotSchemaVersion,
			Actual:   int(payload.SchemaVersion),
		}
	}
	if payload.ProtocolVersion != CurrentVersion {
		return BoardSnapshotPayload{}, &TaskListSnapshotVersionMismatchError{
			Field:    "protocol_version",
			Expected: int(CurrentVersion),
			Actual:   int(payload.ProtocolVersion),
		}
	}
	if payload.LastCheckedAt.IsZero() {
		return BoardSnapshotPayload{}, fmt.Errorf("board snapshot missing last_checked_at")
	}
	if !payload.Freshness.Valid() {
		return BoardSnapshotPayload{}, fmt.Errorf("board snapshot freshness mismatch: expected one of [%s %s], actual %q", TaskListFreshnessFresh, TaskListFreshnessStale, payload.Freshness)
	}
	if payload.Projection.View.ID != "" || payload.Projection.View.Title != "" || len(payload.Projection.View.Columns) > 0 {
		if err := payload.Projection.View.Validate(); err != nil {
			return BoardSnapshotPayload{}, fmt.Errorf("board snapshot view invalid: %w", err)
		}
		viewGroups := make(map[domain.BoardColumnID]struct{}, len(payload.Projection.View.Columns))
		for _, column := range payload.Projection.View.Columns {
			viewGroups[column.ID] = struct{}{}
		}
		for _, group := range payload.Projection.Groups {
			if _, ok := viewGroups[group.GroupID]; !ok {
				return BoardSnapshotPayload{}, fmt.Errorf("board snapshot projection contains unknown group %q", group.GroupID)
			}
		}
	}
	if err := payload.Projection.Validate(); err != nil {
		return BoardSnapshotPayload{}, fmt.Errorf("board snapshot projection invalid: %w", err)
	}
	return payload, nil
}

func (p BoardViewProjection) Validate() error {
	switch p.View.Normalized().Layout {
	case domain.BoardViewLayoutColumnBoard, domain.BoardViewLayoutTreeList, domain.BoardViewLayoutHorizontalGrid:
	default:
		return fmt.Errorf("unsupported layout %q", p.View.Layout)
	}
	known := make(map[string]struct{}, len(p.KnownTaskIDs))
	for _, taskID := range p.KnownTaskIDs {
		id := strings.TrimSpace(taskID.String())
		if id == "" {
			return fmt.Errorf("known task id is empty")
		}
		if _, exists := known[id]; exists {
			return fmt.Errorf("duplicate known task id %q", id)
		}
		known[id] = struct{}{}
	}
	items := make(map[string]BoardViewProjectedItem, len(p.Items))
	viewGroups := make(map[domain.BoardColumnID]struct{}, len(p.View.Columns))
	for _, column := range p.View.Columns {
		viewGroups[column.ID] = struct{}{}
	}
	for _, item := range p.Items {
		id := strings.TrimSpace(item.Task.ID.String())
		if id == "" {
			return fmt.Errorf("projected item task id is empty")
		}
		if _, exists := items[id]; exists {
			return fmt.Errorf("duplicate projected item %q", id)
		}
		if _, exists := known[id]; !exists {
			return fmt.Errorf("projected item %q is not a known task", id)
		}
		if item.Depth < 0 {
			return fmt.Errorf("projected item %q has negative depth", id)
		}
		if _, exists := viewGroups[item.GroupID]; !exists {
			return fmt.Errorf("projected item %q references unknown group %q", id, item.GroupID)
		}
		items[id] = item
	}
	grouped := make(map[string]struct{}, len(items))
	seenGroups := make(map[domain.BoardColumnID]struct{}, len(p.Groups))
	for _, group := range p.Groups {
		if strings.TrimSpace(string(group.GroupID)) == "" {
			return fmt.Errorf("projected group id is empty")
		}
		if _, exists := seenGroups[group.GroupID]; exists {
			return fmt.Errorf("duplicate projected group %q", group.GroupID)
		}
		seenGroups[group.GroupID] = struct{}{}
		for _, taskID := range group.TaskIDs {
			id := taskID.String()
			item, exists := items[id]
			if !exists {
				return fmt.Errorf("group %q references unknown item %q", group.GroupID, id)
			}
			if item.GroupID != group.GroupID {
				return fmt.Errorf("item %q group mismatch", id)
			}
			if _, exists := grouped[id]; exists {
				return fmt.Errorf("item %q appears in multiple groups", id)
			}
			grouped[id] = struct{}{}
		}
	}
	if len(grouped) != len(items) {
		return fmt.Errorf("%d projected items are not assigned to exactly one group", len(items)-len(grouped))
	}
	return nil
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneIssueIDPointer(value *naming.IssueID) *naming.IssueID {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneProtocolSession(session *domain.Session) *domain.Session {
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

func cloneProtocolOwnership(ownership *domain.IssueOwnership) *domain.IssueOwnership {
	if ownership == nil {
		return nil
	}
	cloned := *ownership
	if ownership.ExpiresAt != nil {
		expiresAt := *ownership.ExpiresAt
		cloned.ExpiresAt = &expiresAt
	}
	return &cloned
}
