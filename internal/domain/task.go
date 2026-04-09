package domain

import (
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
)

// Dependency represents a task dependency relationship
type Dependency struct {
	ID   naming.IssueID `json:"id"`
	Type DependencyType `json:"dependency_type"`
}

// Task represents a issue
type Task struct {
	ID                    naming.IssueID `json:"id"`
	Title                 string       `json:"title"`
	Description           string       `json:"description,omitempty"`
	Status                Status       `json:"status"`
	Priority              Priority     `json:"priority"`
	Type                  TaskType     `json:"issue_type"`
	ParentID              *naming.IssueID `json:"parent_id,omitempty"`
	Dependencies          []Dependency `json:"dependencies,omitempty"`
	Implementations       []string     `json:"implementations,omitempty"`
	Session               *Session     `json:"session,omitempty"`
	HasTmuxSession        bool         `json:"has_tmux_session,omitempty"`
	HasWorktree           bool         `json:"has_worktree,omitempty"`
	GitAheadCount         int          `json:"git_ahead_count,omitempty"`
	GitBehindCount        int          `json:"git_behind_count,omitempty"`
	HasUncommittedChanges bool         `json:"has_uncommitted_changes,omitempty"`
	GitAdditions          int          `json:"git_additions,omitempty"`
	GitDeletions          int          `json:"git_deletions,omitempty"`
	CreatedAt             time.Time    `json:"created_at"`
	UpdatedAt             time.Time    `json:"updated_at"`
}

// Status represents task status
type Status string

const (
	StatusOpen       Status = "open"
	StatusInProgress Status = "in_progress"
	StatusBlocked    Status = "blocked"
	StatusDone       Status = "closed"
)

// Column returns the kanban column index for this status
func (s Status) Column() int {
	switch s {
	case StatusOpen:
		return 0
	case StatusInProgress:
		return 1
	case StatusBlocked:
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

// Priority represents task priority (0 = highest)
type Priority int

const (
	P0 Priority = iota // Critical
	P1                 // High
	P2                 // Medium
	P3                 // Low
	P4                 // Backlog
)

// String returns priority as string
func (p Priority) String() string {
	return []string{"P0", "P1", "P2", "P3", "P4"}[p]
}

// TaskType represents the type of task
type TaskType string

const (
	TypeTask    TaskType = "task"
	TypeBug     TaskType = "bug"
	TypeFeature TaskType = "feature"
	TypeEpic    TaskType = "epic"
	TypeChore   TaskType = "chore"
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
	default:
		return "?"
	}
}

// String returns the display string
func (t TaskType) String() string {
	return string(t)
}
