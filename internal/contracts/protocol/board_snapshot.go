package protocol

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

const (
	CommandBoardFetch          = "board.fetch"
	BoardSnapshotSchemaVersion = 1
)

// BoardSnapshotPayload is the daemon/client contract for board view snapshots.
//
// Board snapshots are intentionally not domain.Task payloads. They carry the
// fields needed to render and route board actions, but omit long detail fields
// such as description, notes, design, and acceptance.
type BoardSnapshotPayload struct {
	SchemaVersion    uint16             `json:"schema_version" msgpack:"schema_version"`
	ProtocolVersion  Version            `json:"protocol_version" msgpack:"protocol_version"`
	SnapshotRevision uint64             `json:"snapshot_revision" msgpack:"snapshot_revision"`
	ProjectID        naming.ProjectID   `json:"project_id" msgpack:"project_id"`
	LastCheckedAt    time.Time          `json:"last_checked_at" msgpack:"last_checked_at"`
	Freshness        TaskListFreshness  `json:"freshness" msgpack:"freshness"`
	Tasks            []BoardTaskSummary `json:"tasks" msgpack:"tasks"`
}

type BoardTaskSummary struct {
	ID                    naming.IssueID         `json:"id" msgpack:"id"`
	Title                 string                 `json:"title" msgpack:"title"`
	Assignee              string                 `json:"assignee,omitempty" msgpack:"assignee,omitempty"`
	Labels                []string               `json:"labels,omitempty" msgpack:"labels,omitempty"`
	Estimate              *int                   `json:"estimate,omitempty" msgpack:"estimate,omitempty"`
	Status                domain.Status          `json:"status" msgpack:"status"`
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
	return BoardTaskSummary{
		ID:                    task.ID,
		Title:                 task.Title,
		Assignee:              task.Assignee,
		Labels:                append([]string(nil), task.Labels...),
		Estimate:              cloneIntPointer(task.Estimate),
		Status:                task.Status,
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
	return payload, nil
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
