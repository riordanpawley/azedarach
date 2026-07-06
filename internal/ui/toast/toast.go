package toast

import (
	"strings"
	"time"

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
	return r.RenderAt(toasts, width, time.Now())
}

func (r *ToastRenderer) RenderAt(toasts []types.Toast, width int, now time.Time) string {
	if len(toasts) == 0 || width < 1 {
		return ""
	}

	var rendered []string
	toastWidth := notificationWidth(width)
	visible := visibleToasts(toasts)

	for _, t := range visible {
		style := r.styleForLevel(t.Level)
		contentWidth := toastWidth - style.GetHorizontalFrameSize()
		if contentWidth < 1 {
			contentWidth = 1
		}
		message := ansi.Wrap(t.Message, contentWidth, "")
		style = style.
			Width(contentWidth).
			MaxWidth(toastWidth)
		rendered = append(rendered, r.renderBorderCountdown(style.Render(message), t, now))
	}

	return lipgloss.JoinVertical(lipgloss.Right, rendered...)
}

func (r *ToastRenderer) renderBorderCountdown(view string, t types.Toast, now time.Time) string {
	if t.Expires.IsZero() {
		return view
	}
	if now.IsZero() {
		now = time.Now()
	}
	lines := strings.Split(ansi.Strip(view), "\n")
	if len(lines) == 0 {
		return view
	}
	width := lipgloss.Width(lines[0])
	height := len(lines)
	total := borderCellCount(width, height)
	if width < 2 || height < 2 || total == 0 {
		return view
	}

	createdAt := t.CreatedAt
	if createdAt.IsZero() || !createdAt.Before(t.Expires) {
		createdAt = t.Expires.Add(-defaultToastCountdownDuration)
	}
	duration := t.Expires.Sub(createdAt)
	if duration <= 0 {
		duration = defaultToastCountdownDuration
	}
	remaining := t.Expires.Sub(now)
	if remaining < 0 {
		remaining = 0
	}
	spent := int(float64(total) * (1 - float64(remaining)/float64(duration)))
	if spent < 0 {
		spent = 0
	}
	if spent > total {
		spent = total
	}

	active := lipgloss.NewStyle().Foreground(levelColor(t.Level))
	spentStyle := lipgloss.NewStyle().Foreground(styles.Surface1)
	text := lipgloss.NewStyle().Foreground(levelColor(t.Level))

	out := make([]string, 0, len(lines))
	for row, line := range lines {
		var b strings.Builder
		col := 0
		for _, cell := range line {
			cellWidth := ansi.StringWidth(string(cell))
			if cellWidth < 1 {
				continue
			}
			if idx, ok := borderCellIndex(row, col, width, height); ok {
				if idx < spent {
					b.WriteString(spentStyle.Render(string(cell)))
				} else {
					b.WriteString(active.Render(string(cell)))
				}
			} else if strings.TrimSpace(string(cell)) == "" {
				b.WriteRune(cell)
			} else {
				b.WriteString(text.Render(string(cell)))
			}
			col += cellWidth
		}
		out = append(out, b.String())
	}
	return strings.Join(out, "\n")
}

const defaultToastCountdownDuration = 5 * time.Second

func levelColor(level types.ToastLevel) lipgloss.Color {
	switch level {
	case types.ToastSuccess:
		return styles.Green
	case types.ToastWarning:
		return styles.Yellow
	case types.ToastError:
		return styles.Red
	default:
		return styles.Blue
	}
}

func borderCellCount(width, height int) int {
	if width < 1 || height < 1 {
		return 0
	}
	if height == 1 {
		return width
	}
	return 2*width + 2*max(0, height-2)
}

func borderCellIndex(row, col, width, height int) (int, bool) {
	if width < 1 || height < 1 || row < 0 || col < 0 || row >= height || col >= width {
		return 0, false
	}
	if row == 0 {
		return col, true
	}
	sideRows := max(0, height-2)
	if col == width-1 && row < height-1 {
		return width + row - 1, true
	}
	if row == height-1 {
		return width + sideRows + (width - 1 - col), true
	}
	if col == 0 {
		return width + sideRows + width + (height - 2 - row), true
	}
	return 0, false
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
