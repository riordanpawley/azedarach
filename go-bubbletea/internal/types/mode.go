// Package types contains shared types used across the application.
package types

// Mode represents the current editing mode (Helix-style modal editing)
type Mode int

const (
	ModeNormal Mode = iota
	ModeSelect
	ModeSearch
	ModeGoto
	ModeAction
)

// String returns the string representation of the mode
func (m Mode) String() string {
	switch m {
	case ModeNormal:
		return "NORMAL"
	case ModeSelect:
		return "SELECT"
	case ModeSearch:
		return "SEARCH"
	case ModeGoto:
		return "GOTO"
	case ModeAction:
		return "ACTION"
	default:
		return "UNKNOWN"
	}
}
