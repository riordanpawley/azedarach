package domain

import "time"

// Session represents an active Claude session
type Session struct {
	BeadID    string       `json:"bead_id"`
	State     SessionState `json:"state"`
	StartedAt *time.Time   `json:"started_at,omitempty"`
	Worktree  string       `json:"worktree,omitempty"`
	DevServer *DevServer   `json:"dev_server,omitempty"`
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
