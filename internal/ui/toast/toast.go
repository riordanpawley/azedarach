package toast

import (
	"math"
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
	return r.RenderAtWithin(toasts, width, math.MaxInt, now)
}

func (r *ToastRenderer) RenderWithin(toasts []types.Toast, width, height int) string {
	return r.RenderAtWithin(toasts, width, height, time.Now())
}

func (r *ToastRenderer) RenderAtWithin(toasts []types.Toast, width, height int, now time.Time) string {
	if len(toasts) == 0 || width < 1 || height < 1 {
		return ""
	}

	toastWidth := notificationWidth(width)
	visible := visibleToasts(toasts)
	rendered := make([]string, 0, len(visible))
	remainingHeight := height

	for i := len(visible) - 1; i >= 0; i-- {
		t := visible[i]
		style := r.styleForLevel(t.Level)
		styleWidth := toastWidth - style.GetHorizontalBorderSize() - style.GetHorizontalMargins()
		if styleWidth < 1 {
			styleWidth = 1
		}
		textWidth := styleWidth - style.GetHorizontalPadding()
		if textWidth < 1 {
			textWidth = 1
		}
		maxContentLines := remainingHeight - style.GetVerticalFrameSize()
		if maxContentLines < 1 {
			break
		}
		message := ansi.Hardwrap(ansi.Wrap(t.Message, textWidth, ""), textWidth, false)
		message = truncateWrappedLines(message, textWidth, maxContentLines)
		style = style.
			Width(styleWidth).
			MaxWidth(toastWidth)
		view := r.renderBorderCountdown(style.Render(message), t, now)
		_, viewHeight := renderedBlockSize(view)
		if viewHeight > remainingHeight {
			view = truncateBlockHeight(view, remainingHeight)
			_, viewHeight = renderedBlockSize(view)
		}
		if viewHeight < 1 {
			break
		}
		rendered = append(rendered, view)
		remainingHeight -= viewHeight
	}

	reverseStrings(rendered)
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

func truncateWrappedLines(message string, width, maxLines int) string {
	if maxLines < 1 {
		return ""
	}
	lines := strings.Split(message, "\n")
	if len(lines) <= maxLines {
		return message
	}
	lines = lines[:maxLines]
	last := lines[len(lines)-1]
	tail := truncationTail(width)
	tailWidth := ansi.StringWidth(tail)
	if tailWidth == 0 {
		lines[len(lines)-1] = ansi.Truncate(last, width, "")
	} else if width <= tailWidth {
		lines[len(lines)-1] = ansi.Truncate(tail, width, "")
	} else {
		lines[len(lines)-1] = ansi.Truncate(last, width-tailWidth, "") + tail
	}
	return strings.Join(lines, "\n")
}

func truncationTail(width int) string {
	if width <= 0 {
		return ""
	}
	if width < 3 {
		return strings.Repeat(".", width)
	}
	return "..."
}

func renderedBlockSize(view string) (int, int) {
	if view == "" {
		return 0, 0
	}
	lines := strings.Split(view, "\n")
	maxWidth := 0
	for _, line := range lines {
		if w := lipgloss.Width(line); w > maxWidth {
			maxWidth = w
		}
	}
	return maxWidth, len(lines)
}

func truncateBlockHeight(view string, height int) string {
	if height < 1 || view == "" {
		return ""
	}
	lines := strings.Split(view, "\n")
	if len(lines) <= height {
		return view
	}
	return strings.Join(lines[:height], "\n")
}

func reverseStrings(values []string) {
	for i, j := 0, len(values)-1; i < j; i, j = i+1, j-1 {
		values[i], values[j] = values[j], values[i]
	}
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
