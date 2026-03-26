package keybinds

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type Binding struct {
	Key         string
	Description string
}

type Category struct {
	Name     string
	Bindings []Binding
}

type Theme struct {
	HeaderStyle      lipgloss.Style
	SeparatorStyle   lipgloss.Style
	KeyStyle         lipgloss.Style
	DescriptionStyle lipgloss.Style
	FooterStyle      lipgloss.Style
}

func KeyColumnWidth(categories []Category, minWidth int) int {
	width := minWidth
	for _, category := range categories {
		for _, binding := range category.Bindings {
			if len(binding.Key) > width {
				width = len(binding.Key)
			}
		}
	}
	return width
}

func RenderCategories(categories []Category, keyWidth int, theme Theme) string {
	var content strings.Builder
	for i, category := range categories {
		if i > 0 {
			content.WriteString("\n")
		}
		content.WriteString(theme.HeaderStyle.Render(category.Name + ":"))
		content.WriteString("\n")
		content.WriteString(theme.SeparatorStyle.Render(strings.Repeat("─", keyWidth+28)))
		content.WriteString("\n")
		for _, binding := range category.Bindings {
			keyLabel := fmt.Sprintf("%-*s", keyWidth, binding.Key)
			line := "  " + theme.KeyStyle.Render(keyLabel) + "  " + theme.DescriptionStyle.Render(binding.Description)
			content.WriteString(line)
			content.WriteString("\n")
		}
	}
	return content.String()
}

func RenderInline(bindings []Binding, delimiter string, theme Theme) string {
	if delimiter == "" {
		delimiter = " • "
	}
	parts := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		if strings.TrimSpace(binding.Key) == "" || strings.TrimSpace(binding.Description) == "" {
			continue
		}
		parts = append(parts, theme.KeyStyle.Render(binding.Key)+" "+theme.DescriptionStyle.Render(binding.Description))
	}
	return theme.FooterStyle.Render(strings.Join(parts, delimiter))
}

func RenderPlain(bindings []Binding, delimiter string) string {
	if delimiter == "" {
		delimiter = "  "
	}
	parts := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		if strings.TrimSpace(binding.Key) == "" {
			continue
		}
		if strings.TrimSpace(binding.Description) == "" {
			parts = append(parts, binding.Key)
			continue
		}
		parts = append(parts, binding.Key+": "+binding.Description)
	}
	return strings.Join(parts, delimiter)
}
