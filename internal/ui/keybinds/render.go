package keybinds

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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

func normalizeInlineStyle(style lipgloss.Style) lipgloss.Style {
	// Keep color/weight semantics but remove layout-affecting properties
	// that can inject unexpected line breaks in inline keybind rows.
	return style.Copy().Margin(0).Padding(0)
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
	keyStyle := normalizeInlineStyle(theme.KeyStyle)
	descriptionStyle := normalizeInlineStyle(theme.DescriptionStyle)
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
			line := "  " + keyStyle.Render(keyLabel) + "  " + descriptionStyle.Render(binding.Description)
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
	keyStyle := normalizeInlineStyle(theme.KeyStyle)
	descriptionStyle := normalizeInlineStyle(theme.DescriptionStyle)
	footerStyle := normalizeInlineStyle(theme.FooterStyle)
	parts := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		if strings.TrimSpace(binding.Key) == "" || strings.TrimSpace(binding.Description) == "" {
			continue
		}
		parts = append(parts, keyStyle.Render(binding.Key)+" "+descriptionStyle.Render(binding.Description))
	}
	return footerStyle.Render(strings.Join(parts, delimiter))
}

func RenderKeyTable(bindings []Binding, keyWidth int, theme Theme) string {
	return RenderKeyTableWithinWidth(bindings, keyWidth, 0, theme)
}

func RenderKeyTableWithinWidth(bindings []Binding, keyWidth int, maxWidth int, theme Theme) string {
	keyStyle := normalizeInlineStyle(theme.KeyStyle)
	descriptionStyle := normalizeInlineStyle(theme.DescriptionStyle)
	footerStyle := normalizeInlineStyle(theme.FooterStyle)
	if keyWidth <= 0 {
		keyWidth = 8
		for _, binding := range bindings {
			if len(binding.Key) > keyWidth {
				keyWidth = len(binding.Key)
			}
		}
	}

	lines := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		if strings.TrimSpace(binding.Key) == "" {
			continue
		}
		keyLabel := fmt.Sprintf("%-*s", keyWidth, binding.Key)
		keyPart := "  " + keyStyle.Render(keyLabel)
		if strings.TrimSpace(binding.Description) == "" {
			lines = append(lines, keyPart)
			continue
		}
		desc := binding.Description
		if maxWidth > 0 {
			available := max(4, maxWidth-(2+keyWidth+2))
			desc = ansi.Truncate(desc, available, "...")
		}
		lines = append(lines, keyPart+"  "+descriptionStyle.Render(desc))
	}
	return footerStyle.Render(strings.Join(lines, "\n"))
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
