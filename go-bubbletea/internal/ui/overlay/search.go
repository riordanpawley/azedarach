package overlay

import (
	"fmt"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// SearchMsg is emitted on every keystroke for live filtering
type SearchMsg struct {
	Query string
}

// SearchOverlay provides a search input overlay
type SearchOverlay struct {
	input      textinput.Model
	matchCount int
}

var searchStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("252")).
	Background(lipgloss.Color("235"))

var matchCountStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("240")).
	Background(lipgloss.Color("235"))

// NewSearchOverlay creates a new search overlay
func NewSearchOverlay() *SearchOverlay {
	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "search..."
	ti.Focus()
	ti.CharLimit = 100
	ti.Width = 50

	return &SearchOverlay{
		input:      ti,
		matchCount: 0,
	}
}

// SetMatchCount updates the match count display
func (s *SearchOverlay) SetMatchCount(count int) {
	s.matchCount = count
}

// Init implements tea.Model
func (s *SearchOverlay) Init() tea.Cmd {
	return textinput.Blink
}

// Update implements tea.Model
func (s *SearchOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEnter:
			// Enter closes overlay but keeps filter active
			return s, func() tea.Msg { return CloseOverlayMsg{} }

		case tea.KeyEsc:
			// Esc closes and clears filter
			s.input.SetValue("")
			return s, tea.Batch(
				func() tea.Msg { return SearchMsg{Query: ""} },
				func() tea.Msg { return CloseOverlayMsg{} },
			)
		}
	}

	// Update the text input
	prevValue := s.input.Value()
	s.input, cmd = s.input.Update(msg)

	// Emit SearchMsg if value changed
	if s.input.Value() != prevValue {
		return s, tea.Batch(
			cmd,
			func() tea.Msg { return SearchMsg{Query: s.input.Value()} },
		)
	}

	return s, cmd
}

// View implements tea.Model
func (s *SearchOverlay) View() string {
	inputView := s.input.View()

	// Add match count if there's a query
	if s.input.Value() != "" {
		countText := fmt.Sprintf(" (%d matches)", s.matchCount)
		inputView += matchCountStyle.Render(countText)
	}

	return searchStyle.Render(inputView)
}

// Title implements Overlay interface (returns empty for search bar)
func (s *SearchOverlay) Title() string {
	return ""
}

// Size implements Overlay interface (full-width single line)
func (s *SearchOverlay) Size() (width, height int) {
	return 0, 1
}
