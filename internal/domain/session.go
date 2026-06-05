package domain

import (
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
)

// Session represents an active Claude session
type Session struct {
	IssueID           naming.IssueID `json:"issue_id"`
	State             SessionState   `json:"state"`
	Activity          string         `json:"activity,omitempty"`
	ActivitySource    string         `json:"activity_source,omitempty"`
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
	if s == nil {
		return SessionIdle.Icon()
	}
	if displayState, ok := s.DisplayState(); ok {
		return displayState.Icon()
	}
	if activity := s.DisplayActivity(); activity != "" {
		return SessionState(activity).Icon()
	}
	if s.IsPartial() {
		return "◒"
	}
	return s.State.Icon()
}

func (s *Session) DisplayLabel() string {
	if s == nil {
		return string(SessionIdle)
	}
	if activity := s.DisplayActivity(); activity != "" {
		return activity
	}
	if s.IsPartial() {
		return "partial"
	}
	return string(s.State)
}

func (s *Session) DisplayCode() string {
	if s == nil {
		return "I"
	}
	if displayState, ok := s.DisplayState(); ok {
		return sessionStateDisplayCode(displayState)
	}
	if activity := s.DisplayActivity(); activity != "" {
		return "?"
	}
	if s.IsPartial() {
		return "M"
	}
	return sessionStateDisplayCode(s.State)
}

func (s *Session) DisplayActivity() string {
	if s == nil {
		return ""
	}
	activity := strings.ToLower(strings.TrimSpace(s.Activity))
	switch activity {
	case string(SessionBusy), string(SessionIdle), "unknown":
		return activity
	default:
		return ""
	}
}

func (s *Session) DisplayState() (SessionState, bool) {
	switch s.DisplayActivity() {
	case string(SessionBusy):
		return SessionBusy, true
	case string(SessionIdle):
		return SessionIdle, true
	default:
		return "", false
	}
}

func sessionStateDisplayCode(state SessionState) string {
	switch state {
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
