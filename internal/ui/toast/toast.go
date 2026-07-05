package toast

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

const (
	maxVisibleToasts = 3
	minToastWidth    = 24
	maxToastWidth    = 48
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

// Render renders the latest visible toasts as a bounded stack.
// Returns empty string if no toasts to display
func (r *ToastRenderer) Render(toasts []types.Toast, width int) string {
	if len(toasts) == 0 || width < 1 {
		return ""
	}

	var rendered []string
	toastWidth := notificationWidth(width)
	visible := visibleToasts(toasts)

	for _, t := range visible {
		message := ansi.Wrap(t.Message, toastWidth, "")
		style := r.styleForLevel(t.Level).
			Width(toastWidth).
			MaxWidth(toastWidth)
		rendered = append(rendered, style.Render(message))
	}

	return lipgloss.JoinVertical(lipgloss.Right, rendered...)
}

func visibleToasts(toasts []types.Toast) []types.Toast {
	if len(toasts) <= maxVisibleToasts {
		return toasts
	}
	return toasts[len(toasts)-maxVisibleToasts:]
}

func notificationWidth(width int) int {
	if width < 1 {
		return 1
	}
	toastWidth := width / 3
	if toastWidth < minToastWidth {
		toastWidth = min(width, minToastWidth)
	}
	if toastWidth > maxToastWidth {
		toastWidth = maxToastWidth
	}
	return toastWidth
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
