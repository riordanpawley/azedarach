// Package domain contains core business types for the Azedarach application.
package domain

import "time"

// Status represents the workflow status of a task
type Status int

const (
	StatusOpen Status = iota
	StatusInProgress
	StatusBlocked
	StatusDone
)

func (s Status) String() string {
	return [...]string{"open", "in_progress", "blocked", "done"}[s]
}

// Priority represents task priority
type Priority int

const (
	PriorityNone Priority = iota
	PriorityLow
	PriorityMedium
	PriorityHigh
	PriorityCritical
)

func (p Priority) String() string {
	return [...]string{"none", "low", "medium", "high", "critical"}[p]
}

// IssueType categorizes the type of work
type IssueType int

const (
	TypeTask IssueType = iota
	TypeBug
	TypeFeature
	TypeEpic
	TypeDoc
)

func (t IssueType) String() string {
	return [...]string{"task", "bug", "feature", "epic", "doc"}[t]
}

// Task represents a bead/issue in the system
type Task struct {
	ID          string
	Title       string
	Description string
	Notes       string
	Status      Status
	Priority    Priority
	IssueType   IssueType
	ParentID    *string // For epic children
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// State represents the current state of a Claude session
type State int

const (
	StateIdle State = iota
	StateBusy
	StateWaiting
	StatePaused
	StateDone
	StateError
)

func (s State) String() string {
	return [...]string{"idle", "busy", "waiting", "paused", "done", "error"}[s]
}

// SessionState tracks the state of a Claude session for a bead
type SessionState struct {
	BeadID       string
	State        State
	StartedAt    *time.Time
	LastOutput   *string
	WorktreePath *string
	TmuxSession  *string
}

// Project represents a project that can be managed by Azedarach
type Project struct {
	Name string
	Path string
}

// DevServerState tracks a dev server instance
type DevServerState struct {
	Name        string
	Running     bool
	Port        *int
	WindowName  string
	TmuxSession *string
}
