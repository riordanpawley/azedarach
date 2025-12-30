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

// BeadsError represents an error from the beads CLI
type BeadsError struct {
	Op      string // Operation: "list", "create", "update", etc.
	BeadID  string // Optional: specific bead ID
	Message string // Human-readable context
	Err     error  // Underlying error
}

func (e *BeadsError) Error() string {
	if e.BeadID != "" {
		return fmt.Sprintf("beads %s [%s]: %s", e.Op, e.BeadID, e.Message)
	}
	if e.Message != "" {
		return fmt.Sprintf("beads %s: %s", e.Op, e.Message)
	}
	if e.Err != nil {
		return fmt.Sprintf("beads %s: %v", e.Op, e.Err)
	}
	return fmt.Sprintf("beads %s failed", e.Op)
}

func (e *BeadsError) Unwrap() error {
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
