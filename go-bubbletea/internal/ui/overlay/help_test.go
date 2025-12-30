package overlay

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestNewHelpOverlay(t *testing.T) {
	help := NewHelpOverlay()

	if help == nil {
		t.Fatal("NewHelpOverlay returned nil")
	}

	if help.styles == nil {
		t.Error("styles should be initialized")
	}

	if help.scroll != 0 {
		t.Errorf("initial scroll should be 0, got %d", help.scroll)
	}
}

func TestHelpOverlay_Title(t *testing.T) {
	help := NewHelpOverlay()
	title := help.Title()

	if title != "Help" {
		t.Errorf("expected title 'Help', got '%s'", title)
	}
}

func TestHelpOverlay_Size(t *testing.T) {
	help := NewHelpOverlay()
	width, height := help.Size()

	if width <= 0 || height <= 0 {
		t.Errorf("size should be positive, got width=%d, height=%d", width, height)
	}

	// Verify reasonable dimensions
	if width < 40 {
		t.Errorf("width seems too small: %d", width)
	}

	if height < 20 {
		t.Errorf("height seems too small: %d", height)
	}
}

func TestHelpOverlay_View_ContainsKeyBindings(t *testing.T) {
	help := NewHelpOverlay()
	help.viewHeight = 100 // Set large enough to show all content

	view := help.View()

	// Check that view contains expected category names
	expectedCategories := []string{"Navigation", "Actions", "Modes", "Selection", "Other"}
	for _, category := range expectedCategories {
		if !strings.Contains(view, category) {
			t.Errorf("view should contain category '%s'", category)
		}
	}

	// Check that view contains some key bindings from different categories
	expectedBindings := []string{
		"h/l",        // Navigation
		"j/k",        // Navigation
		"Space",      // Actions
		"Enter",      // Actions
		"/",          // Modes
		"?",          // Modes
		"Tab",        // Other
		"Quit",       // Other
	}

	for _, binding := range expectedBindings {
		if !strings.Contains(view, binding) {
			t.Errorf("view should contain binding '%s'", binding)
		}
	}
}

func TestHelpOverlay_Init(t *testing.T) {
	help := NewHelpOverlay()
	cmd := help.Init()

	if cmd != nil {
		t.Error("Init should return nil")
	}
}

func TestHelpOverlay_Update_EscapeCloses(t *testing.T) {
	help := NewHelpOverlay()

	tests := []struct {
		name string
		key  string
	}{
		{"escape key", "esc"},
		{"q key", "q"},
		{"? key", "?"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, cmd := help.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tt.key)})

			if cmd == nil {
				t.Error("expected command to close overlay")
				return
			}

			msg := cmd()
			if _, ok := msg.(CloseOverlayMsg); !ok {
				t.Errorf("expected CloseOverlayMsg, got %T", msg)
			}
		})
	}
}

func TestHelpOverlay_Update_ScrollDown(t *testing.T) {
	help := NewHelpOverlay()
	help.viewHeight = 5 // Set small height to enable scrolling
	help.maxScroll = 10 // Simulate content that can be scrolled

	initialScroll := help.scroll

	// Send 'j' key to scroll down
	model, _ := help.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	help = model.(*HelpOverlay)

	if help.scroll <= initialScroll {
		t.Error("scroll should increase after pressing 'j'")
	}
}

func TestHelpOverlay_Update_ScrollUp(t *testing.T) {
	help := NewHelpOverlay()
	help.scroll = 5     // Start scrolled down
	help.viewHeight = 5
	help.maxScroll = 10

	initialScroll := help.scroll

	// Send 'k' key to scroll up
	model, _ := help.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	help = model.(*HelpOverlay)

	if help.scroll >= initialScroll {
		t.Error("scroll should decrease after pressing 'k'")
	}
}

func TestHelpOverlay_Update_ScrollBounds(t *testing.T) {
	help := NewHelpOverlay()
	help.scroll = 0
	help.viewHeight = 5
	help.maxScroll = 10

	// Try scrolling up from position 0 (should not go negative)
	model, _ := help.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	help = model.(*HelpOverlay)

	if help.scroll < 0 {
		t.Errorf("scroll should not be negative, got %d", help.scroll)
	}

	// Scroll to maximum
	help.scroll = help.maxScroll

	// Try scrolling down beyond maximum (should not exceed maxScroll)
	model, _ = help.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	help = model.(*HelpOverlay)

	if help.scroll > help.maxScroll {
		t.Errorf("scroll should not exceed maxScroll (%d), got %d", help.maxScroll, help.scroll)
	}
}

func TestHelpOverlay_Update_JumpToTop(t *testing.T) {
	help := NewHelpOverlay()
	help.scroll = 10
	help.maxScroll = 20

	// Send 'g' key to jump to top
	model, _ := help.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	help = model.(*HelpOverlay)

	if help.scroll != 0 {
		t.Errorf("expected scroll to be 0 after 'g', got %d", help.scroll)
	}
}

func TestHelpOverlay_Update_JumpToBottom(t *testing.T) {
	help := NewHelpOverlay()
	help.scroll = 0
	help.maxScroll = 20

	// Send 'G' key to jump to bottom
	model, _ := help.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	help = model.(*HelpOverlay)

	if help.scroll != help.maxScroll {
		t.Errorf("expected scroll to be %d after 'G', got %d", help.maxScroll, help.scroll)
	}
}

func TestHelpOverlay_GetCategories(t *testing.T) {
	help := NewHelpOverlay()
	categories := help.getCategories()

	if len(categories) == 0 {
		t.Error("getCategories should return at least one category")
	}

	// Verify each category has a name and bindings
	for i, cat := range categories {
		if cat.Name == "" {
			t.Errorf("category %d has empty name", i)
		}

		if len(cat.Bindings) == 0 {
			t.Errorf("category '%s' has no bindings", cat.Name)
		}

		// Verify bindings have required fields
		for j, binding := range cat.Bindings {
			if binding.Key == "" {
				t.Errorf("binding %d in category '%s' has empty key", j, cat.Name)
			}
			if binding.Description == "" {
				t.Errorf("binding %d in category '%s' has empty description", j, cat.Name)
			}
		}
	}
}

func TestHelpOverlay_ScrollIndicator(t *testing.T) {
	help := NewHelpOverlay()
	help.viewHeight = 3   // Very small viewport to force scrolling
	help.maxScroll = 10   // Content requires scrolling

	view := help.View()

	// When maxScroll > 0, view should contain scroll hints
	if !strings.Contains(view, "j/k") && !strings.Contains(view, "scroll") {
		t.Error("view should show scroll indicators when content is scrollable")
	}
}

func TestMinMaxHelpers(t *testing.T) {
	tests := []struct {
		name     string
		a, b     int
		wantMin  int
		wantMax  int
	}{
		{"positive numbers", 5, 10, 5, 10},
		{"negative numbers", -5, -10, -10, -5},
		{"mixed signs", -5, 10, -5, 10},
		{"equal values", 7, 7, 7, 7},
		{"zero values", 0, 5, 0, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMin := min(tt.a, tt.b)
			if gotMin != tt.wantMin {
				t.Errorf("min(%d, %d) = %d, want %d", tt.a, tt.b, gotMin, tt.wantMin)
			}

			gotMax := max(tt.a, tt.b)
			if gotMax != tt.wantMax {
				t.Errorf("max(%d, %d) = %d, want %d", tt.a, tt.b, gotMax, tt.wantMax)
			}
		})
	}
}
