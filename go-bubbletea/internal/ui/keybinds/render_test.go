package keybinds

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestKeyColumnWidth(t *testing.T) {
	categories := []Category{
		{
			Name: "A",
			Bindings: []Binding{
				{Key: "x", Description: "one"},
				{Key: "ctrl+shift+x", Description: "two"},
			},
		},
	}

	if got := KeyColumnWidth(categories, 4); got != len("ctrl+shift+x") {
		t.Fatalf("KeyColumnWidth = %d, want %d", got, len("ctrl+shift+x"))
	}
}

func TestRenderInlineSkipsInvalidBindings(t *testing.T) {
	theme := Theme{
		KeyStyle:         lipgloss.NewStyle(),
		DescriptionStyle: lipgloss.NewStyle(),
		FooterStyle:      lipgloss.NewStyle(),
	}
	rendered := RenderInline([]Binding{
		{Key: "j/k", Description: "move"},
		{Key: "", Description: "skip"},
		{Key: "esc", Description: "close"},
	}, " | ", theme)

	if !strings.Contains(rendered, "j/k move") || !strings.Contains(rendered, "esc close") {
		t.Fatalf("rendered = %q, want valid bindings", rendered)
	}
	if strings.Contains(rendered, "skip") {
		t.Fatalf("rendered = %q, should skip invalid binding", rendered)
	}
}

func TestRenderCategoriesContainsHeadersAndRows(t *testing.T) {
	theme := Theme{
		HeaderStyle:      lipgloss.NewStyle(),
		SeparatorStyle:   lipgloss.NewStyle(),
		KeyStyle:         lipgloss.NewStyle(),
		DescriptionStyle: lipgloss.NewStyle(),
	}
	rendered := RenderCategories([]Category{
		{
			Name: "Navigation",
			Bindings: []Binding{
				{Key: "j/k", Description: "move"},
			},
		},
	}, 8, theme)

	if !strings.Contains(rendered, "Navigation:") {
		t.Fatalf("rendered = %q, want category header", rendered)
	}
	if !strings.Contains(rendered, "j/k") || !strings.Contains(rendered, "move") {
		t.Fatalf("rendered = %q, want binding row", rendered)
	}
}

func TestRenderPlain(t *testing.T) {
	rendered := RenderPlain([]Binding{
		{Key: "h/l", Description: "columns"},
		{Key: "j/k", Description: "tasks"},
		{Key: "", Description: "skip"},
	}, "  ")
	want := "h/l: columns  j/k: tasks"
	if rendered != want {
		t.Fatalf("RenderPlain = %q, want %q", rendered, want)
	}
}

func TestRenderKeyTable(t *testing.T) {
	theme := Theme{
		KeyStyle:         lipgloss.NewStyle(),
		DescriptionStyle: lipgloss.NewStyle(),
		FooterStyle:      lipgloss.NewStyle(),
	}
	rendered := RenderKeyTable([]Binding{
		{Key: "j/k", Description: "move"},
		{Key: "Esc", Description: "close"},
	}, 8, theme)
	if !strings.Contains(rendered, "j/k") || !strings.Contains(rendered, "move") {
		t.Fatalf("rendered = %q, want key table entries", rendered)
	}
	if !strings.Contains(rendered, "\n") {
		t.Fatalf("rendered = %q, want multiline table", rendered)
	}
}
