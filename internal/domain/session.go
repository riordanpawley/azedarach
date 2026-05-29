package domain

import (
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
)

// Session represents an active Claude session
type Session struct {
	IssueID           naming.IssueID `json:"issue_id"`
	State             SessionState   `json:"state"`
	TotalCount        int            `json:"total_count,omitempty"`
	ActiveCount       int            `json:"active_count,omitempty"`
	PausedCount       int            `json:"paused_count,omitempty"`
	TmuxAttached      bool           `json:"tmux_attached,omitempty"`
	TmuxAttachedCount int            `json:"tmux_attached_count,omitempty"`
	StartedAt         *time.Time     `json:"started_at,omitempty"`
	Worktree          string         `json:"worktree,omitempty"`
	DevServer         *DevServer     `json:"dev_server,omitempty"`
}

// SessionState represents the current state of a session
type SessionState string

const (
	SessionIdle    SessionState = "idle"
	SessionBusy    SessionState = "busy"
	SessionWaiting SessionState = "waiting"
	SessionDone    SessionState = "done"
	SessionError   SessionState = "error"
	SessionPaused  SessionState = "paused"
)

// Icon returns a unicode icon for the state
func (s SessionState) Icon() string {
	switch s {
	case SessionIdle:
		return "○"
	case SessionBusy:
		return "●"
	case SessionWaiting:
		return "◐"
	case SessionDone:
		return "✓"
	case SessionError:
		return "✗"
	case SessionPaused:
		return "⏸"
	default:
		return "?"
	}
}

func (s *Session) IsPartial() bool {
	if s == nil {
		return false
	}
	return s.ActiveCount > 0 && s.PausedCount > 0
}

func (s *Session) DisplayIcon() string {
	if s != nil && s.IsPartial() {
		return "◒"
	}
	if s == nil {
		return SessionIdle.Icon()
	}
	return s.State.Icon()
}

func (s *Session) DisplayLabel() string {
	if s == nil {
		return string(SessionIdle)
	}
	if s.IsPartial() {
		return "partial"
	}
	return string(s.State)
}

func (s *Session) DisplayCode() string {
	if s != nil && s.IsPartial() {
		return "M"
	}
	if s == nil {
		return "I"
	}
	switch s.State {
	case SessionBusy:
		return "B"
	case SessionWaiting:
		return "W"
	case SessionDone:
		return "D"
	case SessionError:
		return "E"
	case SessionPaused:
		return "P"
	case SessionIdle:
		return "I"
	default:
		return "?"
	}
}

// String returns the display string
func (s SessionState) String() string {
	return string(s)
}

// DevServer represents a running dev server
type DevServer struct {
	Port    int    `json:"port"`
	Command string `json:"command"`
	Running bool   `json:"running"`
}

// Project represents a project that can be managed by Azedarach
type Project struct {
	Name string `json:"name"`
	Path string `json:"path"`
}
