package toast

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// ToastRenderer handles rendering of toast notifications
type ToastRenderer struct {
	styles *styles.Styles
}

// New creates a new ToastRenderer with the given styles
func New(styles *styles.Styles) *ToastRenderer {
	return &ToastRenderer{
		styles: styles,
	}
}

// Render renders a stack of toasts in the bottom-right corner
// Returns empty string if no toasts to display
func (r *ToastRenderer) Render(toasts []types.Toast, width int) string {
	if len(toasts) == 0 {
		return ""
	}

	var rendered []string
	toastWidth := width / 3
	if toastWidth > 40 {
		toastWidth = 40 // Cap maximum toast width
	}

	for _, t := range toasts {
		style := r.styleForLevel(t.Level)
		rendered = append(rendered, style.Width(toastWidth).Render(t.Message))
	}

	// Stack toasts vertically, aligned to the right
	return lipgloss.JoinVertical(lipgloss.Right, rendered...)
}

// styleForLevel returns the appropriate style for a toast level
func (r *ToastRenderer) styleForLevel(level types.ToastLevel) lipgloss.Style {
	switch level {
	case types.ToastSuccess:
		return r.styles.ToastSuccess
	case types.ToastWarning:
		return r.styles.ToastWarning
	case types.ToastError:
		return r.styles.ToastError
	default:
		return r.styles.ToastInfo
	}
}
