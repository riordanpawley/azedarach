package domain

import (
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
)

// DependencyType represents the type of dependency relationship
type DependencyType string

const (
	DependencyBlocks      DependencyType = "blocks"
	DependencyBlockedBy   DependencyType = "blocked-by"
	DependencyRelatedTo   DependencyType = "related"
	DependencyParentChild DependencyType = "parent-child"
	DependencyDiscovered  DependencyType = "discovered-from"
	DependencyCreatedIn   DependencyType = "created-in"
)

// Dependency represents a task dependency relationship
type Dependency struct {
	ID   naming.IssueID `json:"id"`
	Type DependencyType `json:"dependency_type"`
}

// Task represents a issue
type Task struct {
	ID                    naming.IssueID  `json:"id"`
	Title                 string          `json:"title"`
	Description           string          `json:"description,omitempty"`
	Notes                 string          `json:"notes,omitempty"`
	Design                string          `json:"design,omitempty"`
	Acceptance            string          `json:"acceptance,omitempty"`
	Assignee              string          `json:"assignee,omitempty"`
	Labels                []string        `json:"labels,omitempty"`
	Estimate              *int            `json:"estimate,omitempty"`
	Status                Status          `json:"status"`
	State                 IssueState      `json:"issue_state,omitzero" msgpack:"issue_state,omitempty"`
	Facts                 IssueFacts      `json:"issue_facts,omitzero" msgpack:"issue_facts,omitempty"`
	Priority              Priority        `json:"priority"`
	Type                  TaskType        `json:"issue_type"`
	ParentID              *naming.IssueID `json:"parent_id,omitempty"`
	Dependencies          []Dependency    `json:"dependencies,omitempty"`
	Implementations       []string        `json:"implementations,omitempty"`
	Session               *Session        `json:"session,omitempty"`
	HasTmuxSession        bool            `json:"has_tmux_session,omitempty"`
	HasWorktree           bool            `json:"has_worktree,omitempty"`
	GitAheadCount         int             `json:"git_ahead_count,omitempty"`
	GitBehindCount        int             `json:"git_behind_count,omitempty"`
	HasUncommittedChanges bool            `json:"has_uncommitted_changes,omitempty"`
	HasConflicts          bool            `json:"has_conflicts,omitempty"`
	ConflictFiles         []string        `json:"conflict_files,omitempty"`
	GitAdditions          int             `json:"git_additions,omitempty"`
	GitDeletions          int             `json:"git_deletions,omitempty"`
	Origin                string          `json:"origin,omitempty"`
	PullRequest           *PullRequest    `json:"pull_request,omitempty"`
	RuntimeUpdatedAt      time.Time       `json:"runtime_updated_at,omitempty,omitzero"`
	Ownership             *IssueOwnership `json:"ownership,omitempty"`
	CreatedAt             time.Time       `json:"created_at"`
	UpdatedAt             time.Time       `json:"updated_at"`
}

// PullRequest is compact GitHub PR metadata attached to an issue for board
// rendering and CLI/TUI status surfaces.
type PullRequest struct {
	Number       int    `json:"number,omitempty"`
	RemoteKey    string `json:"remote_key,omitempty"`
	DisplayKey   string `json:"display_key,omitempty"`
	URL          string `json:"url,omitempty"`
	State        string `json:"state,omitempty"`
	Draft        bool   `json:"draft,omitempty"`
	ChecksStatus string `json:"checks_status,omitempty"`
}

// IssueOwnership is a durable issue claim used to prevent duplicate agent pickup.
type IssueOwnership struct {
	OwnerID   string     `json:"owner_id"`
	OwnerKind string     `json:"owner_kind"`
	ClaimedAt time.Time  `json:"claimed_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func (o IssueOwnership) IsExpired(now time.Time) bool {
	return o.ExpiresAt != nil && !o.ExpiresAt.IsZero() && !now.Before(*o.ExpiresAt)
}

func (o IssueOwnership) IsActive(now time.Time) bool {
	return strings.TrimSpace(o.OwnerID) != "" && !o.IsExpired(now)
}

func (o IssueOwnership) OwnedBy(ownerID string, now time.Time) bool {
	return o.IsActive(now) && strings.EqualFold(strings.TrimSpace(o.OwnerID), strings.TrimSpace(ownerID))
}

func (o IssueOwnership) BlocksActor(ownerID string, now time.Time) bool {
	return o.IsActive(now) && !strings.EqualFold(strings.TrimSpace(o.OwnerID), strings.TrimSpace(ownerID))
}

// Status represents task status
type Status string

const (
	StatusOpen       Status = "open"
	StatusInProgress Status = "in_progress"
	StatusInReview   Status = "in_review"
	StatusDone       Status = "closed"
)

// Column returns the kanban column index for this status
func (s Status) Column() int {
	switch s {
	case StatusOpen:
		return 0
	case StatusInProgress:
		return 1
	case StatusInReview:
		return 2
	case StatusDone:
		return 3
	default:
		return 0
	}
}

// String returns the display string
func (s Status) String() string {
	return string(s)
}

func BoardStatusForTask(task Task) Status {
	if status := task.IssueFacts().DisplayStatus; status != "" {
		return status
	}
	return task.Status
}

// Priority represents task priority (0 = highest)
type Priority int

const (
	P0 Priority = iota // Critical
	P1                 // High
	P2                 // Medium
	P3                 // Low
	P4                 // Lowest
)

// String returns priority as string
func (p Priority) String() string {
	return []string{"P0", "P1", "P2", "P3", "P4"}[p]
}

// TaskType represents the type of task
type TaskType string

const (
	TypeTask          TaskType = "task"
	TypeBug           TaskType = "bug"
	TypeFeature       TaskType = "feature"
	TypeEpic          TaskType = "epic"
	TypeChore         TaskType = "chore"
	TypeInvestigation TaskType = "investigation"
)

// Short returns single character representation
func (t TaskType) Short() string {
	switch t {
	case TypeTask:
		return "T"
	case TypeBug:
		return "B"
	case TypeFeature:
		return "F"
	case TypeEpic:
		return "E"
	case TypeChore:
		return "C"
	case TypeInvestigation:
		return "I"
	default:
		return "?"
	}
}

// String returns the display string
func (t TaskType) String() string {
	return string(t)
}
