package domain

import (
	"errors"
	"fmt"
)

// Sentinel errors
var (
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrOffline      = errors.New("offline")
	ErrUserCanceled = errors.New("user canceled")
)

// TaskStoreError represents an error from task store operations.
type TaskStoreError struct {
	Op      string // Operation: "list", "create", "update", etc.
	TaskID  string // Optional: specific task/issue ID
	IssueID string // Deprecated compatibility alias for TaskID
	Message string // Human-readable context
	Err     error  // Underlying error
}

func (e *TaskStoreError) Error() string {
	id := e.TaskID
	if id == "" {
		id = e.IssueID
	}
	if id != "" {
		return fmt.Sprintf("taskstore %s [%s]: %s", e.Op, id, e.Message)
	}
	if e.Message != "" {
		return fmt.Sprintf("taskstore %s: %s", e.Op, e.Message)
	}
	if e.Err != nil {
		return fmt.Sprintf("taskstore %s: %v", e.Op, e.Err)
	}
	return fmt.Sprintf("taskstore %s failed", e.Op)
}

func (e *TaskStoreError) Unwrap() error {
	return e.Err
}

// TmuxError represents an error from tmux operations
type TmuxError struct {
	Op      string
	Session string
	Err     error
}

func (e *TmuxError) Error() string {
	if e.Session != "" {
		return fmt.Sprintf("tmux %s [%s]: %v", e.Op, e.Session, e.Err)
	}
	return fmt.Sprintf("tmux %s: %v", e.Op, e.Err)
}

func (e *TmuxError) Unwrap() error {
	return e.Err
}

// GitError represents an error from git operations
type GitError struct {
	Op       string
	Worktree string
	Err      error
}

func (e *GitError) Error() string {
	if e.Worktree != "" {
		return fmt.Sprintf("git %s [%s]: %v", e.Op, e.Worktree, e.Err)
	}
	return fmt.Sprintf("git %s: %v", e.Op, e.Err)
}

func (e *GitError) Unwrap() error {
	return e.Err
}
