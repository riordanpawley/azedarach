package overlay

import (
	"fmt"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

const defaultJumpLabelChars = "abcdefghijklmnopqrstuvwxyz"

// GenerateLabels generates jump labels for the given count
// using the default label character set.
func GenerateLabels(count int) []string {
	return GenerateLabelsWithChars(count, defaultJumpLabelChars)
}

// GenerateLabelsWithChars generates jump labels for the given count using
// single-character labels first, then double-character combinations.
func GenerateLabelsWithChars(count int, chars string) []string {
	if count <= 0 {
		return []string{}
	}
	normalizedChars := normalizeJumpChars(chars)
	if len(normalizedChars) == 0 {
		normalizedChars = []rune(defaultJumpLabelChars)
	}

	labels := make([]string, 0, count)

	// Single-character labels first (fast access).
	for i := 0; i < count && i < len(normalizedChars); i++ {
		labels = append(labels, string(normalizedChars[i]))
	}

	if len(labels) >= count {
		return labels
	}

	// Double-character labels using the configured set.
	for first := 0; first < len(normalizedChars) && len(labels) < count; first++ {
		for second := 0; second < len(normalizedChars) && len(labels) < count; second++ {
			label := string(normalizedChars[first]) + string(normalizedChars[second])
			labels = append(labels, label)
		}
	}

	return labels
}

// JumpMode is an overlay that shows jump labels for quick navigation
type JumpMode struct {
	labels      map[string]int // label -> task index (flat across all columns)
	indexLabels []string       // task index -> label
	input       string         // accumulated input
	maxLen      int            // maximum label length
	allowedKeys map[rune]struct{}
	styles      *Styles
}

// JumpSelectedMsg is sent when a jump target is selected
type JumpSelectedMsg struct {
	TaskIndex int
}

// NewJumpMode creates a new jump mode overlay with labels
func NewJumpMode(taskCount int) *JumpMode {
	return NewJumpModeWithChars(taskCount, defaultJumpLabelChars)
}

// NewJumpModeWithChars creates a new jump mode overlay with labels using the provided key set.
func NewJumpModeWithChars(taskCount int, chars string) *JumpMode {
	labels := GenerateLabelsWithChars(taskCount, chars)
	labelMap := make(map[string]int)
	indexLabels := make([]string, 0, len(labels))
	allowedKeys := make(map[rune]struct{})

	maxLen := 1
	for i, label := range labels {
		labelMap[label] = i
		indexLabels = append(indexLabels, label)
		if len(label) > maxLen {
			maxLen = len(label)
		}
	}
	for _, r := range normalizeJumpChars(chars) {
		allowedKeys[r] = struct{}{}
	}
	if len(allowedKeys) == 0 {
		for _, r := range defaultJumpLabelChars {
			allowedKeys[r] = struct{}{}
		}
	}

	return &JumpMode{
		labels:      labelMap,
		indexLabels: indexLabels,
		input:       "",
		maxLen:      maxLen,
		allowedKeys: allowedKeys,
		styles:      New(),
	}
}

// Init initializes the jump mode
func (j *JumpMode) Init() tea.Cmd {
	return nil
}

// Update handles messages
func (j *JumpMode) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			return j, func() tea.Msg { return CloseOverlayMsg{} }

		case "backspace":
			if len(j.input) > 0 {
				j.input = j.input[:len(j.input)-1]
			}
			return j, nil

		default:
			// Accumulate input
			key := msg.String()
			if len(key) == 1 {
				r := unicode.ToLower(rune(key[0]))
				if _, ok := j.allowedKeys[r]; !ok {
					return j, nil
				}
				j.input += string(r)

				// Check for exact match
				if index, ok := j.labels[j.input]; ok {
					// Only select if we've reached max length OR there's no longer match possible
					shouldSelect := len(j.input) >= j.maxLen || !j.hasLongerMatch()

					if shouldSelect {
						return j, func() tea.Msg {
							return JumpSelectedMsg{TaskIndex: index}
						}
					}
				}

				// Check if input is getting too long
				if len(j.input) >= j.maxLen {
					// No match found, reset input
					j.input = ""
				}

				return j, nil
			}
		}
	}

	return j, nil
}

// View renders the jump mode overlay
func (j *JumpMode) View() string {
	var b strings.Builder

	// Title
	title := j.styles.Title.Render("Jump Mode")
	b.WriteString(title)
	b.WriteString("\n\n")

	// Current input
	if j.input == "" {
		hint := j.styles.MenuItem.Foreground(styles.Overlay1).Render("Type a label to jump...")
		b.WriteString(hint)
	} else {
		inputStyle := lipgloss.NewStyle().
			Foreground(styles.Yellow).
			Bold(true).
			Background(styles.Surface1).
			Padding(0, 1)
		b.WriteString("Input: ")
		b.WriteString(inputStyle.Render(j.input))
	}

	b.WriteString("\n\n")

	// Show available labels
	labelList := j.getLabelList()
	if len(labelList) > 0 {
		labelStyle := lipgloss.NewStyle().Foreground(styles.Subtext0)
		b.WriteString(labelStyle.Render("Available: "))

		// Show first 20 labels as preview
		preview := labelList
		if len(preview) > 20 {
			preview = labelList[:20]
		}

		for i, label := range preview {
			if i > 0 {
				b.WriteString(" ")
			}
			keyStyle := j.styles.MenuKey
			b.WriteString(keyStyle.Render(label))
		}

		if len(labelList) > 20 {
			more := fmt.Sprintf(" ... +%d more", len(labelList)-20)
			b.WriteString(j.styles.Footer.Render(more))
		}
	}

	b.WriteString("\n\n")

	// Footer
	footer := j.styles.Footer.Render("Type label • Backspace: delete • Esc: cancel")
	b.WriteString(footer)

	return b.String()
}

// Title returns the overlay title
func (j *JumpMode) Title() string {
	return "Jump"
}

// Size returns the overlay dimensions
func (j *JumpMode) Size() (width, height int) {
	return 50, 10
}

// GetLabel returns the label for a given task index
func (j *JumpMode) GetLabel(index int) string {
	if index < 0 || index >= len(j.indexLabels) {
		return ""
	}
	return j.indexLabels[index]
}

// getLabelList returns a sorted list of labels
func (j *JumpMode) getLabelList() []string {
	labels := make([]string, 0, len(j.labels))
	for label := range j.labels {
		labels = append(labels, label)
	}

	// Sort by index to maintain order
	// Simple bubble sort since we're just displaying
	for i := 0; i < len(labels); i++ {
		for k := i + 1; k < len(labels); k++ {
			if j.labels[labels[i]] > j.labels[labels[k]] {
				labels[i], labels[k] = labels[k], labels[i]
			}
		}
	}

	return labels
}

// hasLongerMatch checks if there are any labels that start with current input
// and are longer than the current input
func (j *JumpMode) hasLongerMatch() bool {
	for label := range j.labels {
		if len(label) > len(j.input) && strings.HasPrefix(label, j.input) {
			return true
		}
	}
	return false
}

// isJumpKey checks if a rune is valid for jump labels (home row or alphabet)
func isJumpKey(r rune) bool {
	r = unicode.ToLower(r)
	return r >= 'a' && r <= 'z'
}

// isHomeRowKey is kept for backwards compatibility
func isHomeRowKey(r rune) bool {
	return isJumpKey(r)
}

func normalizeJumpChars(chars string) []rune {
	seen := make(map[rune]struct{})
	out := make([]rune, 0, len(chars))
	for _, raw := range chars {
		r := unicode.ToLower(raw)
		if unicode.IsSpace(r) {
			continue
		}
		if _, exists := seen[r]; exists {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return out
}

// RenderLabel renders a jump label with styling
func RenderLabel(label string) string {
	style := lipgloss.NewStyle().
		Foreground(styles.Base).
		Background(styles.Yellow).
		Bold(true).
		Padding(0, 1)
	return style.Render(label)
}
