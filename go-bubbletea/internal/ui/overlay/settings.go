package overlay

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// SettingType represents the type of a setting
type SettingType int

const (
	// SettingToggle is a boolean on/off setting (Space/Enter to toggle)
	SettingToggle SettingType = iota
	// SettingChoice is a multiple-choice setting (Left/Right to cycle)
	SettingChoice
	// SettingAction is an action that triggers something (Enter to activate)
	SettingAction
	// SettingSeparator is a visual separator (not selectable)
	SettingSeparator
)

// SettingItem represents a single setting in the settings menu
type SettingItem struct {
	Key      string
	Label    string
	Type     SettingType
	Value    any
	Choices  []string      // For SettingChoice type
	OnChange func(any)     // Callback when value changes
	OnAction func() tea.Cmd // Callback for SettingAction type
}

// SettingsOverlay is a settings menu overlay
type SettingsOverlay struct {
	items  []SettingItem
	cursor int
	styles *Styles
}

// NewSettingsOverlay creates a new settings overlay with the given items
func NewSettingsOverlay(items []SettingItem) *SettingsOverlay {
	s := New()
	menu := &SettingsOverlay{
		items:  items,
		cursor: 0,
		styles: s,
	}
	// Position cursor on first selectable item
	menu.moveCursorToNextSelectable()
	return menu
}

// NewDefaultSettingsOverlay creates a settings overlay with default app settings
func NewDefaultSettingsOverlay() *SettingsOverlay {
	items := []SettingItem{
		{
			Key:   "refresh",
			Label: "Auto-refresh beads",
			Type:  SettingToggle,
			Value: true,
			OnChange: func(value any) {
				// TODO: Wire this to config
			},
		},
		{
			Key:   "compact",
			Label: "Compact card view",
			Type:  SettingToggle,
			Value: false,
			OnChange: func(value any) {
				// TODO: Wire this to config
			},
		},
		{
			Key:   "theme",
			Label: "Theme",
			Type:  SettingChoice,
			Value: "macchiato",
			Choices: []string{
				"latte",
				"frappe",
				"macchiato",
				"mocha",
			},
			OnChange: func(value any) {
				// TODO: Wire this to config
			},
		},
		{
			Key:      "",
			Label:    "───────────────────",
			Type:     SettingSeparator,
			Value:    nil,
			OnChange: nil,
		},
		{
			Key:   "editor",
			Label: "Open config in $EDITOR",
			Type:  SettingAction,
			Value: nil,
			OnAction: func() tea.Cmd {
				return openConfigInEditor()
			},
		},
		{
			Key:   "projects",
			Label: "Manage projects",
			Type:  SettingAction,
			Value: nil,
			OnAction: func() tea.Cmd {
				// This will be handled by the app to open project selector
				return func() tea.Msg {
					return SelectionMsg{
						Key:   "projects",
						Value: nil,
					}
				}
			},
		},
	}

	return NewSettingsOverlay(items)
}

// Init initializes the overlay
func (m *SettingsOverlay) Init() tea.Cmd {
	return nil
}

// Update handles messages
func (m *SettingsOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			return m, func() tea.Msg { return CloseOverlayMsg{} }

		case "j", "down":
			m.moveCursorDown()
			return m, nil

		case "k", "up":
			m.moveCursorUp()
			return m, nil

		case "h", "left":
			return m, m.decrementChoice()

		case "l", "right":
			return m, m.incrementChoice()

		case " ", "space":
			return m, m.toggleOrActivate()

		case "enter":
			return m, m.activateCurrent()
		}
	}

	return m, nil
}

// View renders the settings menu
func (m *SettingsOverlay) View() string {
	var b strings.Builder

	for i, item := range m.items {
		// Separators
		if item.Type == SettingSeparator {
			b.WriteString(m.styles.Separator.Render(item.Label))
			b.WriteString("\n")
			continue
		}

		// Determine style based on cursor position
		var style, keyStyle = m.styles.MenuItem, m.styles.MenuKey
		if i == m.cursor {
			style = m.styles.MenuItemActive
		}

		// Format line based on type
		var line string
		switch item.Type {
		case SettingToggle:
			valueStr := "off"
			if v, ok := item.Value.(bool); ok && v {
				valueStr = "on"
			}
			line = fmt.Sprintf("%s %s [%s]",
				keyStyle.Render("["+item.Key+"]"),
				style.Render(item.Label),
				style.Render(valueStr),
			)

		case SettingChoice:
			valueStr := ""
			if v, ok := item.Value.(string); ok {
				valueStr = v
			}
			line = fmt.Sprintf("%s %s <%s>",
				keyStyle.Render("["+item.Key+"]"),
				style.Render(item.Label),
				style.Render(valueStr),
			)

		case SettingAction:
			line = fmt.Sprintf("%s %s",
				keyStyle.Render("["+item.Key+"]"),
				style.Render(item.Label),
			)
		}

		b.WriteString(line)
		b.WriteString("\n")
	}

	// Add footer hint
	b.WriteString("\n")
	b.WriteString(m.styles.Footer.Render("j/k: navigate • h/l: change choice • space/enter: toggle/activate • esc: close"))

	return b.String()
}

// Title returns the overlay title
func (m *SettingsOverlay) Title() string {
	return "Settings"
}

// Size returns the overlay dimensions
func (m *SettingsOverlay) Size() (width, height int) {
	// Width: enough for longest setting line
	// Height: number of items + footer + padding
	return 60, len(m.items) + 6
}

// moveCursorDown moves the cursor to the next selectable item
func (m *SettingsOverlay) moveCursorDown() {
	for i := 1; i <= len(m.items); i++ {
		next := (m.cursor + i) % len(m.items)
		if m.items[next].Type != SettingSeparator {
			m.cursor = next
			return
		}
	}
}

// moveCursorUp moves the cursor to the previous selectable item
func (m *SettingsOverlay) moveCursorUp() {
	for i := 1; i <= len(m.items); i++ {
		prev := (m.cursor - i + len(m.items)) % len(m.items)
		if m.items[prev].Type != SettingSeparator {
			m.cursor = prev
			return
		}
	}
}

// moveCursorToNextSelectable moves cursor to first selectable item from current position
func (m *SettingsOverlay) moveCursorToNextSelectable() {
	for i := 0; i < len(m.items); i++ {
		if m.items[i].Type != SettingSeparator {
			m.cursor = i
			return
		}
	}
}

// toggleOrActivate toggles a toggle setting or activates an action
func (m *SettingsOverlay) toggleOrActivate() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return nil
	}

	item := &m.items[m.cursor]

	switch item.Type {
	case SettingToggle:
		// Toggle boolean value
		if v, ok := item.Value.(bool); ok {
			item.Value = !v
			if item.OnChange != nil {
				item.OnChange(item.Value)
			}
		}
		return nil

	case SettingAction:
		// Trigger action
		if item.OnAction != nil {
			return item.OnAction()
		}
		return nil

	default:
		return nil
	}
}

// activateCurrent activates the current item (for actions or toggles)
func (m *SettingsOverlay) activateCurrent() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return nil
	}

	item := &m.items[m.cursor]

	switch item.Type {
	case SettingToggle:
		return m.toggleOrActivate()

	case SettingAction:
		if item.OnAction != nil {
			return item.OnAction()
		}
		return nil

	default:
		return nil
	}
}

// incrementChoice increments the choice value (wrapping around)
func (m *SettingsOverlay) incrementChoice() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return nil
	}

	item := &m.items[m.cursor]

	if item.Type != SettingChoice {
		return nil
	}

	if len(item.Choices) == 0 {
		return nil
	}

	// Find current value index
	currentIdx := -1
	if v, ok := item.Value.(string); ok {
		for i, choice := range item.Choices {
			if choice == v {
				currentIdx = i
				break
			}
		}
	}

	// Move to next choice (wrap around)
	nextIdx := (currentIdx + 1) % len(item.Choices)
	item.Value = item.Choices[nextIdx]

	if item.OnChange != nil {
		item.OnChange(item.Value)
	}

	return nil
}

// decrementChoice decrements the choice value (wrapping around)
func (m *SettingsOverlay) decrementChoice() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return nil
	}

	item := &m.items[m.cursor]

	if item.Type != SettingChoice {
		return nil
	}

	if len(item.Choices) == 0 {
		return nil
	}

	// Find current value index
	currentIdx := -1
	if v, ok := item.Value.(string); ok {
		for i, choice := range item.Choices {
			if choice == v {
				currentIdx = i
				break
			}
		}
	}

	// Move to previous choice (wrap around)
	prevIdx := (currentIdx - 1 + len(item.Choices)) % len(item.Choices)
	item.Value = item.Choices[prevIdx]

	if item.OnChange != nil {
		item.OnChange(item.Value)
	}

	return nil
}

// openConfigInEditor opens the config file in $EDITOR
func openConfigInEditor() tea.Cmd {
	return func() tea.Msg {
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vim" // Default to vim
		}

		// Get config path
		home, err := os.UserHomeDir()
		if err != nil {
			return SelectionMsg{
				Key:   "editor-error",
				Value: fmt.Errorf("failed to get home directory: %w", err),
			}
		}

		configPath := home + "/.config/azedarach/config.toml"

		// Create the command
		cmd := exec.Command(editor, configPath)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		// Run the editor
		if err := cmd.Run(); err != nil {
			return SelectionMsg{
				Key:   "editor-error",
				Value: fmt.Errorf("failed to open editor: %w", err),
			}
		}

		return SelectionMsg{
			Key:   "editor-closed",
			Value: nil,
		}
	}
}

// NewSettingsOverlayWithEditor creates a settings overlay with editor service integration
func NewSettingsOverlayWithEditor(editor interface {
	GetShowPhases() bool
	ToggleShowPhases()
}) *SettingsOverlay {
	items := []SettingItem{
		{
			Key:   "phases",
			Label: "Show dependency phases",
			Type:  SettingToggle,
			Value: editor.GetShowPhases(),
			OnChange: func(value any) {
				editor.ToggleShowPhases()
			},
		},
		{
			Key:   "refresh",
			Label: "Auto-refresh beads",
			Type:  SettingToggle,
			Value: true,
			OnChange: func(value any) {
				// TODO: Wire this to config
			},
		},
		{
			Key:   "compact",
			Label: "Compact card view",
			Type:  SettingToggle,
			Value: false,
			OnChange: func(value any) {
				// TODO: Wire this to config
			},
		},
		{
			Key:   "theme",
			Label: "Theme",
			Type:  SettingChoice,
			Value: "macchiato",
			Choices: []string{
				"latte",
				"frappe",
				"macchiato",
				"mocha",
			},
			OnChange: func(value any) {
				// TODO: Wire this to config
			},
		},
		{
			Key:      "",
			Label:    "───────────────────",
			Type:     SettingSeparator,
			Value:    nil,
			OnChange: nil,
		},
		{
			Key:   "editor",
			Label: "Open config in $EDITOR",
			Type:  SettingAction,
			Value: nil,
			OnAction: func() tea.Cmd {
				return openConfigInEditor()
			},
		},
		{
			Key:   "projects",
			Label: "Manage projects",
			Type:  SettingAction,
			Value: nil,
			OnAction: func() tea.Cmd {
				// This will be handled by the app to open project selector
				return func() tea.Msg {
					return SelectionMsg{
						Key:   "projects",
						Value: nil,
					}
				}
			},
		},
	}

	return NewSettingsOverlay(items)
}
