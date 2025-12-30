package overlay

import tea "github.com/charmbracelet/bubbletea"

// Overlay represents a modal overlay component
type Overlay interface {
	tea.Model
	Title() string
	Size() (width, height int)
}

// CloseOverlayMsg signals that the overlay should be closed
type CloseOverlayMsg struct{}

// SelectionMsg is sent when an action is selected
type SelectionMsg struct {
	Key   string
	Value any
}
