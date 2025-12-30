package styles

import (
	"testing"
)

func TestNew(t *testing.T) {
	s := New()
	if s == nil {
		t.Fatal("New() returned nil")
	}
}

func TestPriorityBadge(t *testing.T) {
	s := New()

	tests := []struct {
		priority int
		name     string
	}{
		{0, "P0 Critical"},
		{1, "P1 High"},
		{2, "P2 Medium"},
		{3, "P3 Low"},
		{4, "P4 Backlog"},
		{5, "Out of bounds (should use last color)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			style := s.PriorityBadge(tt.priority)
			// Style should be non-zero (have some properties set)
			rendered := style.Render("P0")
			if len(rendered) == 0 {
				t.Error("PriorityBadge rendered empty string")
			}
		})
	}
}

func TestThemeColors(t *testing.T) {
	// Verify colors are defined
	colors := []struct {
		name  string
		color string
	}{
		{"Base", string(Base)},
		{"Blue", string(Blue)},
		{"Red", string(Red)},
		{"Green", string(Green)},
		{"Yellow", string(Yellow)},
	}

	for _, c := range colors {
		t.Run(c.name, func(t *testing.T) {
			if c.color == "" {
				t.Errorf("%s color is empty", c.name)
			}
			// Catppuccin colors start with #
			if c.color[0] != '#' {
				t.Errorf("%s color doesn't start with #: %s", c.name, c.color)
			}
		})
	}
}
