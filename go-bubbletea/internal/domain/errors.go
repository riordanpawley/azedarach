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

// IssueTrackerError represents an error from the issue tracker adapter
type IssueTrackerError struct {
	Op      string // Operation: "list", "create", "update", etc.
	IssueID string // Optional: specific issue ID
	Message string // Human-readable context
	Err     error  // Underlying error
}

func (e *IssueTrackerError) Error() string {
	if e.IssueID != "" {
		return fmt.Sprintf("issue tracker %s [%s]: %s", e.Op, e.IssueID, e.Message)
	}
	if e.Message != "" {
		return fmt.Sprintf("issue tracker %s: %s", e.Op, e.Message)
	}
	if e.Err != nil {
		return fmt.Sprintf("issue tracker %s: %v", e.Op, e.Err)
	}
	return fmt.Sprintf("issue tracker %s failed", e.Op)
}

func (e *IssueTrackerError) Unwrap() error {
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
