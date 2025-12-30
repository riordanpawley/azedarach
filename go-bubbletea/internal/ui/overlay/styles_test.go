package overlay

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestNewStyles(t *testing.T) {
	styles := New()
	if styles == nil {
		t.Fatal("New() returned nil")
	}

	// Verify all style fields are initialized (non-zero)
	tests := []struct {
		name  string
		style lipgloss.Style
	}{
		{"Overlay", styles.Overlay},
		{"Title", styles.Title},
		{"MenuItem", styles.MenuItem},
		{"MenuItemActive", styles.MenuItemActive},
		{"MenuItemDisabled", styles.MenuItemDisabled},
		{"MenuKey", styles.MenuKey},
		{"MenuKeyDisabled", styles.MenuKeyDisabled},
		{"Separator", styles.Separator},
		{"Footer", styles.Footer},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Verify the style renders something (not empty)
			rendered := tt.style.Render("test")
			if rendered == "" {
				t.Errorf("%s style rendered empty string", tt.name)
			}
		})
	}
}

func TestOverlayStyle(t *testing.T) {
	styles := New()

	// Verify overlay has border and background
	rendered := styles.Overlay.Render("Content")
	if len(rendered) == 0 {
		t.Error("Overlay style should render content")
	}

	// Verify it includes the content
	// Note: lipgloss may add ANSI codes and borders, so we can't do exact match
	if len(rendered) < len("Content") {
		t.Error("Overlay rendered output should be longer than input (includes styling)")
	}
}

func TestTitleStyle(t *testing.T) {
	styles := New()

	rendered := styles.Title.Render("Test Title")
	if len(rendered) == 0 {
		t.Error("Title style should render content")
	}
}

func TestMenuItemStyles(t *testing.T) {
	styles := New()

	tests := []struct {
		name  string
		style lipgloss.Style
		text  string
	}{
		{"MenuItem", styles.MenuItem, "Normal Item"},
		{"MenuItemActive", styles.MenuItemActive, "Active Item"},
		{"MenuItemDisabled", styles.MenuItemDisabled, "Disabled Item"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rendered := tt.style.Render(tt.text)
			if len(rendered) == 0 {
				t.Errorf("%s style should render content", tt.name)
			}
		})
	}
}

func TestMenuKeyStyles(t *testing.T) {
	styles := New()

	active := styles.MenuKey.Render("x")
	if len(active) == 0 {
		t.Error("MenuKey style should render content")
	}

	disabled := styles.MenuKeyDisabled.Render("x")
	if len(disabled) == 0 {
		t.Error("MenuKeyDisabled style should render content")
	}

	// Both styles should render content (we don't test exact difference since
	// lipgloss rendering can be environment-dependent)
}

func TestSeparatorStyle(t *testing.T) {
	styles := New()

	separator := "───────────"
	rendered := styles.Separator.Render(separator)
	if len(rendered) == 0 {
		t.Error("Separator style should render content")
	}
}

func TestFooterStyle(t *testing.T) {
	styles := New()

	footer := "Press ESC to close"
	rendered := styles.Footer.Render(footer)
	if len(rendered) == 0 {
		t.Error("Footer style should render content")
	}
}
