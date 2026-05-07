package overlay

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
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
	Key        string
	Group      string
	Label      string
	Type       SettingType
	Value      any
	Choices    []string       // For SettingChoice type
	ActionHint string         // Optional display hint for action rows
	OnChange   func(any)      // Callback when value changes
	OnAction   func() tea.Cmd // Callback for SettingAction type
}

// SettingsOverlay is a settings menu overlay
type SettingsOverlay struct {
	twoPaneDialogChrome
	dialogViewportState
	items        []SettingItem
	cursor       int
	configSource string
	config       *config.Config
	configPath   string
	styles       *Styles
}

// NewSettingsOverlay creates a new settings overlay with the given items
func NewSettingsOverlay(items []SettingItem) *SettingsOverlay {
	return NewSettingsOverlayWithSource(items, "")
}

// NewSettingsOverlayWithSource creates a settings overlay with optional config source path.
func NewSettingsOverlayWithSource(items []SettingItem, configSource string) *SettingsOverlay {
	s := New()
	menu := &SettingsOverlay{
		items:        items,
		cursor:       0,
		configSource: configSource,
		styles:       s,
	}
	// Position cursor on first selectable item
	menu.moveCursorToNextSelectable()
	return menu
}

func NewSettingsOverlayWithConfig(items []SettingItem, cfg *config.Config, configSource string) *SettingsOverlay {
	menu := NewSettingsOverlayWithSource(items, configSource)
	menu.config = cfg
	menu.configPath = configSource
	return menu
}

// NewDefaultSettingsOverlay creates a settings overlay with default app settings
func NewDefaultSettingsOverlay() *SettingsOverlay {
	items := []SettingItem{
		{
			Key:   "refresh",
			Group: "General",
			Label: "Auto-refresh issues",
			Type:  SettingToggle,
			Value: true,
			OnChange: func(value any) {
				// TODO: Wire this to config
			},
		},
		{
			Key:   "compact",
			Group: "General",
			Label: "Compact card view",
			Type:  SettingToggle,
			Value: false,
			OnChange: func(value any) {
				// TODO: Wire this to config
			},
		},
		{
			Key:   "theme",
			Group: "General",
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
			Group:    "Actions",
			Label:    "───────────────────",
			Type:     SettingSeparator,
			Value:    nil,
			OnChange: nil,
		},
		{
			Key:        "editor",
			Group:      "Actions",
			Label:      "Open config in $EDITOR",
			Type:       SettingAction,
			Value:      nil,
			ActionHint: "open",
			OnAction: func() tea.Cmd {
				return openConfigEditorMsg()
			},
		},
		{
			Key:        "projects",
			Group:      "Actions",
			Label:      "Manage projects",
			Type:       SettingAction,
			Value:      nil,
			ActionHint: "open",
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

		case "e":
			if cmd := m.activateByKey("editor"); cmd != nil {
				return m, cmd
			}
			return m, nil
		}
	case tea.WindowSizeMsg:
		m.ApplyWindowSize(msg)
	}

	return m, nil
}

// View renders the settings menu
func (m *SettingsOverlay) View() string {
	var b strings.Builder

	if strings.TrimSpace(m.configSource) != "" {
		b.WriteString(m.styles.MenuItemDisabled.Render("Config source: " + m.configSource))
		b.WriteString("\n\n")
	}

	lastGroup := ""
	for i, item := range m.items {
		if item.Group != "" && item.Group != lastGroup {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(m.styles.MenuHeader.Render(item.Group))
			b.WriteString("\n")
			lastGroup = item.Group
		}

		// Separators
		if item.Type == SettingSeparator {
			b.WriteString(m.styles.Separator.Render(item.Label))
			b.WriteString("\n")
			continue
		}

		// Determine style based on cursor position
		var style = m.styles.MenuItem
		if i == m.cursor {
			style = m.styles.MenuItemActive
		}

		// Format line based on type
		prefix := "  "
		if i == m.cursor {
			prefix = "▶ "
		}
		label := item.Label
		if item.Group != "" {
			label = "  " + label
		}
		dots := strings.Repeat(".", max(1, 36-len([]rune(label))))

		var line, value string
		switch item.Type {
		case SettingToggle:
			valueStr := "no"
			if v, ok := item.Value.(bool); ok && v {
				valueStr = "yes"
			}
			value = valueStr

		case SettingChoice:
			valueStr := ""
			if v, ok := item.Value.(string); ok {
				valueStr = v
			}
			value = valueStr

		case SettingAction:
			value = item.ActionHint
			if value == "" {
				value = "run"
			}
		}

		line = style.Render(prefix+label) +
			m.styles.MenuItemDisabled.Render(dots) +
			m.styles.MenuKey.Render(value)

		if item.Key != "" {
			line = line + " " + m.styles.MenuItemDisabled.Render("["+item.Key+"]")
		}

		b.WriteString(line)
		b.WriteString("\n")
	}

	width, height := m.Size()
	return renderDialogTwoPane(dialogLayoutConfig{
		styles:            m.styles,
		width:             width,
		height:            height,
		title:             "Settings",
		rightSectionTitle: "Actions",
		breakpoint:        68,
		gap:               2,
		minLeft:           50,
		minRight:          15,
		leftFocused:       true,
		renderLeft: func(mode dialogLayoutMode, width, height int) string {
			return b.String()
		},
		renderRight: func(mode dialogLayoutMode, width, height int) string {
			return renderDialogActions(m.styles, []keybinds.Binding{
				{Key: "j/k", Description: "move"},
				{Key: "h/l", Description: "cycle"},
				{Key: "Enter/Space", Description: "toggle/activate"},
				{Key: "e", Description: "edit config"},
				{Key: "Esc", Description: "close"},
			})
		},
	})
}

// Title returns the overlay title
func (m *SettingsOverlay) Title() string {
	return "Settings"
}

// Size returns the overlay dimensions
func (m *SettingsOverlay) Size() (width, height int) {
	// Width: enough for longest setting line
	// Height: number of items + footer + padding
	return m.ClampResponsive(72, len(m.items)+9)
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
			return m.persistConfig()
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

func (m *SettingsOverlay) activateByKey(key string) tea.Cmd {
	for i := range m.items {
		if m.items[i].Key != key {
			continue
		}
		if m.items[i].Type != SettingAction || m.items[i].OnAction == nil {
			return nil
		}
		return m.items[i].OnAction()
	}
	return nil
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

	return m.persistConfig()
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

	return m.persistConfig()
}

func (m *SettingsOverlay) persistConfig() tea.Cmd {
	if m.config == nil || strings.TrimSpace(m.configPath) == "" {
		return nil
	}

	cfg := m.config
	path := m.configPath
	return func() tea.Msg {
		if err := config.SaveConfig(cfg, path); err != nil {
			return SelectionMsg{
				Key:   "settings-save-error",
				Value: err,
			}
		}
		return nil
	}
}

func openConfigEditorMsg() tea.Cmd {
	return func() tea.Msg {
		return SelectionMsg{
			Key:   "editor",
			Value: nil,
		}
	}
}

// NewSettingsOverlayWithEditor creates a settings overlay with editor service integration.
func NewSettingsOverlayWithEditor(editor interface {
	GetShowPhases() bool
	ToggleShowPhases()
}) *SettingsOverlay {
	return NewSettingsOverlayWithEditorAndSource(editor, "")
}

// NewSettingsOverlayWithEditorAndSource creates a settings overlay with editor service integration.
// configSource is rendered as informational context near the top of the overlay.
func NewSettingsOverlayWithEditorAndSource(editor interface {
	GetShowPhases() bool
	ToggleShowPhases()
}, configSource string) *SettingsOverlay {
	return NewSettingsOverlayWithEditorAndConfig(editor, nil, configSource)
}

// NewSettingsOverlayWithEditorAndConfig creates a config-backed settings overlay with editor service integration.
func NewSettingsOverlayWithEditorAndConfig(editor interface {
	GetShowPhases() bool
	ToggleShowPhases()
}, cfg *config.Config, configSource string) *SettingsOverlay {
	cliTool := "claude"
	if cfg != nil && strings.TrimSpace(cfg.CLITool) != "" {
		cliTool = cfg.CLITool
	}
	skipPermissions := false
	if cfg != nil {
		skipPermissions = cfg.Session.DangerouslySkipPermissions
	}
	gitPushEnabled := true
	gitFetchEnabled := true
	prDraftByDefault := true
	prAutoLink := true
	prNotifyAfterCreate := true
	prCreateWithoutMerge := false
	networkAutoDetect := true
	gitWorkflowMode := "worktree"
	gitDefaultMergeStrategy := "merge"
	gitShowLineChanges := true
	mergeStrategy := "merge"
	mergeAutoMerge := false
	mergeCompareWithOrigin := true
	networkCheckInterval := 60
	networkOfflineTimeout := 300
	networkRetryAttempts := 3
	notificationsCompletedTask := true
	notificationsFailedTask := true
	notificationsErrorThreshold := 3
	worktreeAutoCleanup := true
	worktreeKeepDays := 7
	specEnabled := true
	if cfg != nil {
		gitPushEnabled = cfg.Git.PushEnabled
		gitFetchEnabled = cfg.Git.FetchEnabled
		gitWorkflowMode = cfg.Git.WorkflowMode
		gitDefaultMergeStrategy = cfg.Git.DefaultMergeStrategy
		gitShowLineChanges = cfg.Git.ShowLineChanges
		prDraftByDefault = cfg.PR.DraftByDefault
		prAutoLink = cfg.PR.AutoLink
		prNotifyAfterCreate = cfg.PR.NotifyAfterCreate
		prCreateWithoutMerge = cfg.PR.CreateWithoutMerge
		networkAutoDetect = cfg.Network.AutoDetect
		networkCheckInterval = cfg.Network.CheckInterval
		networkOfflineTimeout = cfg.Network.OfflineTimeout
		networkRetryAttempts = cfg.Network.RetryAttempts
		mergeStrategy = cfg.Merge.Strategy
		mergeAutoMerge = cfg.Merge.AutoMerge
		mergeCompareWithOrigin = cfg.Merge.CompareWithOrigin
		notificationsCompletedTask = cfg.Notifications.CompletedTask
		notificationsFailedTask = cfg.Notifications.FailedTask
		notificationsErrorThreshold = cfg.Notifications.ErrorThreshold
		worktreeAutoCleanup = cfg.Worktree.AutoCleanup
		worktreeKeepDays = cfg.Worktree.KeepDays
		specEnabled = cfg.Spec.Enabled
	}
	items := []SettingItem{
		{
			Key:   "cli-tool",
			Group: "Session",
			Label: "CLI Tool",
			Type:  SettingChoice,
			Value: cliTool,
			Choices: []string{
				"claude",
				"opencode",
				"codex",
			},
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				if v, ok := value.(string); ok {
					cfg.CLITool = v
				}
			},
		},
		{
			Key:   "skip-permissions",
			Group: "Session",
			Label: "Skip permissions",
			Type:  SettingToggle,
			Value: skipPermissions,
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				if v, ok := value.(bool); ok {
					cfg.Session.DangerouslySkipPermissions = v
				}
			},
		},
		{
			Key:   "session-timeout-ms",
			Group: "Session",
			Label: "Session timeout (ms)",
			Type:  SettingChoice,
			Value: fmt.Sprintf("%d", sessionTimeout(cfg)),
			Choices: []string{
				"15000",
				"30000",
				"60000",
				"120000",
			},
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				cfg.Session.TimeoutMs = parseIntChoice(value, cfg.Session.TimeoutMs)
			},
		},
		{
			Key:   "phases",
			Group: "General",
			Label: "Show dependency phases",
			Type:  SettingToggle,
			Value: editor.GetShowPhases(),
			OnChange: func(value any) {
				editor.ToggleShowPhases()
			},
		},
		{
			Key:   "git-workflow-mode",
			Group: "Git",
			Label: "Workflow mode",
			Type:  SettingChoice,
			Value: gitWorkflowMode,
			Choices: []string{
				"worktree",
				"branch",
			},
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				if v, ok := value.(string); ok {
					cfg.Git.WorkflowMode = v
				}
			},
		},
		{
			Key:   "git-default-merge-strategy",
			Group: "Git",
			Label: "Default merge strategy",
			Type:  SettingChoice,
			Value: gitDefaultMergeStrategy,
			Choices: []string{
				"merge",
				"squash",
				"rebase",
			},
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				if v, ok := value.(string); ok {
					cfg.Git.DefaultMergeStrategy = v
				}
			},
		},
		{
			Key:   "git-show-line-changes",
			Group: "Git",
			Label: "Show line changes",
			Type:  SettingToggle,
			Value: gitShowLineChanges,
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				if v, ok := value.(bool); ok {
					cfg.Git.ShowLineChanges = v
				}
			},
		},
		{
			Key:   "git-push-enabled",
			Group: "Git",
			Label: "Git Push",
			Type:  SettingToggle,
			Value: gitPushEnabled,
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				if v, ok := value.(bool); ok {
					cfg.Git.PushEnabled = v
				}
			},
		},
		{
			Key:   "git-fetch-enabled",
			Group: "Git",
			Label: "Git Fetch",
			Type:  SettingToggle,
			Value: gitFetchEnabled,
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				if v, ok := value.(bool); ok {
					cfg.Git.FetchEnabled = v
				}
			},
		},
		{
			Key:   "pr-draft-by-default",
			Group: "Pull Requests",
			Label: "Draft by default",
			Type:  SettingToggle,
			Value: prDraftByDefault,
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				if v, ok := value.(bool); ok {
					cfg.PR.DraftByDefault = v
				}
			},
		},
		{
			Key:   "pr-auto-link",
			Group: "Pull Requests",
			Label: "Auto-link PR",
			Type:  SettingToggle,
			Value: prAutoLink,
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				if v, ok := value.(bool); ok {
					cfg.PR.AutoLink = v
				}
			},
		},
		{
			Key:   "pr-notify-after-create",
			Group: "Pull Requests",
			Label: "Notify after create",
			Type:  SettingToggle,
			Value: prNotifyAfterCreate,
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				if v, ok := value.(bool); ok {
					cfg.PR.NotifyAfterCreate = v
				}
			},
		},
		{
			Key:   "pr-create-without-merge",
			Group: "Pull Requests",
			Label: "Create without merge",
			Type:  SettingToggle,
			Value: prCreateWithoutMerge,
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				if v, ok := value.(bool); ok {
					cfg.PR.CreateWithoutMerge = v
				}
			},
		},
		{
			Key:   "network-auto-detect",
			Group: "Network",
			Label: "Auto-detect network",
			Type:  SettingToggle,
			Value: networkAutoDetect,
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				if v, ok := value.(bool); ok {
					cfg.Network.AutoDetect = v
				}
			},
		},
		{
			Key:   "network-check-interval",
			Group: "Network",
			Label: "Check interval (sec)",
			Type:  SettingChoice,
			Value: fmt.Sprintf("%d", networkCheckInterval),
			Choices: []string{
				"15",
				"30",
				"60",
				"120",
				"300",
			},
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				cfg.Network.CheckInterval = parseIntChoice(value, cfg.Network.CheckInterval)
			},
		},
		{
			Key:   "network-offline-timeout",
			Group: "Network",
			Label: "Offline timeout (sec)",
			Type:  SettingChoice,
			Value: fmt.Sprintf("%d", networkOfflineTimeout),
			Choices: []string{
				"60",
				"120",
				"300",
				"600",
			},
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				cfg.Network.OfflineTimeout = parseIntChoice(value, cfg.Network.OfflineTimeout)
			},
		},
		{
			Key:   "network-retry-attempts",
			Group: "Network",
			Label: "Retry attempts",
			Type:  SettingChoice,
			Value: fmt.Sprintf("%d", networkRetryAttempts),
			Choices: []string{
				"1",
				"2",
				"3",
				"5",
				"8",
			},
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				cfg.Network.RetryAttempts = parseIntChoice(value, cfg.Network.RetryAttempts)
			},
		},
		{
			Key:   "merge-strategy",
			Group: "Merge",
			Label: "Merge strategy",
			Type:  SettingChoice,
			Value: mergeStrategy,
			Choices: []string{
				"merge",
				"squash",
				"rebase",
			},
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				if v, ok := value.(string); ok {
					cfg.Merge.Strategy = v
				}
			},
		},
		{
			Key:   "merge-auto-merge",
			Group: "Merge",
			Label: "Auto-merge",
			Type:  SettingToggle,
			Value: mergeAutoMerge,
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				if v, ok := value.(bool); ok {
					cfg.Merge.AutoMerge = v
				}
			},
		},
		{
			Key:   "merge-compare-with-origin",
			Group: "Merge",
			Label: "Compare with origin",
			Type:  SettingToggle,
			Value: mergeCompareWithOrigin,
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				if v, ok := value.(bool); ok {
					cfg.Merge.CompareWithOrigin = v
				}
			},
		},
		{
			Key:   "notifications-completed-task",
			Group: "Notifications",
			Label: "Notify completed task",
			Type:  SettingToggle,
			Value: notificationsCompletedTask,
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				if v, ok := value.(bool); ok {
					cfg.Notifications.CompletedTask = v
				}
			},
		},
		{
			Key:   "notifications-failed-task",
			Group: "Notifications",
			Label: "Notify failed task",
			Type:  SettingToggle,
			Value: notificationsFailedTask,
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				if v, ok := value.(bool); ok {
					cfg.Notifications.FailedTask = v
				}
			},
		},
		{
			Key:   "notifications-error-threshold",
			Group: "Notifications",
			Label: "Error threshold",
			Type:  SettingChoice,
			Value: fmt.Sprintf("%d", notificationsErrorThreshold),
			Choices: []string{
				"1",
				"2",
				"3",
				"5",
				"10",
			},
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				cfg.Notifications.ErrorThreshold = parseIntChoice(value, cfg.Notifications.ErrorThreshold)
			},
		},
		{
			Key:   "worktree-auto-cleanup",
			Group: "Worktree",
			Label: "Auto-cleanup",
			Type:  SettingToggle,
			Value: worktreeAutoCleanup,
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				if v, ok := value.(bool); ok {
					cfg.Worktree.AutoCleanup = v
				}
			},
		},
		{
			Key:   "worktree-keep-days",
			Group: "Worktree",
			Label: "Keep days",
			Type:  SettingChoice,
			Value: fmt.Sprintf("%d", worktreeKeepDays),
			Choices: []string{
				"1",
				"3",
				"7",
				"14",
				"30",
			},
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				cfg.Worktree.KeepDays = parseIntChoice(value, cfg.Worktree.KeepDays)
			},
		},
		{
			Key:   "spec-enabled",
			Group: "Spec",
			Label: "Spec workflow enabled",
			Type:  SettingToggle,
			Value: specEnabled,
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				if v, ok := value.(bool); ok {
					cfg.Spec.Enabled = v
				}
			},
		},
		{
			Key:      "",
			Group:    "Actions",
			Label:    "───────────────────",
			Type:     SettingSeparator,
			Value:    nil,
			OnChange: nil,
		},
		{
			Key:        "editor",
			Group:      "Actions",
			Label:      "Open config in $EDITOR",
			Type:       SettingAction,
			Value:      nil,
			ActionHint: "open",
			OnAction: func() tea.Cmd {
				return openConfigEditorMsg()
			},
		},
		{
			Key:        "projects",
			Group:      "Actions",
			Label:      "Manage projects",
			Type:       SettingAction,
			Value:      nil,
			ActionHint: "open",
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

	return NewSettingsOverlayWithConfig(items, cfg, configSource)
}

func parseIntChoice(value any, fallback int) int {
	v, ok := value.(string)
	if !ok {
		return fallback
	}
	parsed, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return parsed
}

func sessionTimeout(cfg *config.Config) int {
	if cfg == nil || cfg.Session.TimeoutMs <= 0 {
		return config.DefaultConfig().Session.TimeoutMs
	}
	return cfg.Session.TimeoutMs
}
