package overlay

import tea "github.com/charmbracelet/bubbletea"

// Stack manages a stack of overlays with push/pop operations
type Stack struct {
	overlays []Overlay
}

// NewStack creates a new empty overlay stack
func NewStack() *Stack {
	return &Stack{
		overlays: make([]Overlay, 0),
	}
}

// Push adds an overlay to the top of the stack
func (s *Stack) Push(o Overlay) tea.Cmd {
	s.overlays = append(s.overlays, o)
	return o.Init()
}

// Pop removes and returns the top overlay from the stack
// Returns nil if the stack is empty
func (s *Stack) Pop() Overlay {
	if len(s.overlays) == 0 {
		return nil
	}

	top := s.overlays[len(s.overlays)-1]
	s.overlays = s.overlays[:len(s.overlays)-1]
	return top
}

// Current returns the top overlay without removing it
// Returns nil if the stack is empty
func (s *Stack) Current() Overlay {
	if len(s.overlays) == 0 {
		return nil
	}
	return s.overlays[len(s.overlays)-1]
}

// IsEmpty returns true if the stack has no overlays
func (s *Stack) IsEmpty() bool {
	return len(s.overlays) == 0
}

// Clear removes all overlays from the stack
func (s *Stack) Clear() {
	s.overlays = make([]Overlay, 0)
}

// Update forwards the message to the current overlay and handles CloseOverlayMsg
func (s *Stack) Update(msg tea.Msg) tea.Cmd {
	// If stack is empty, nothing to update
	if s.IsEmpty() {
		return nil
	}

	// Check if message is a CloseOverlayMsg
	if _, ok := msg.(CloseOverlayMsg); ok {
		s.Pop()
		return nil
	}

	// Forward message to current overlay
	current := s.Current()
	newModel, cmd := current.Update(msg)

	// Update the overlay in the stack
	if len(s.overlays) > 0 {
		if newOverlay, ok := newModel.(Overlay); ok {
			s.overlays[len(s.overlays)-1] = newOverlay
		}
	}

	return cmd
}
