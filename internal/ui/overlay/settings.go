package overlay

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/latencytrace"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
)

const settingsDefaultSaveTargetKey = "settings-default-save-target"

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
	ConfigPath []string       // JSON path for config-backed settings
	SaveTarget string         // "local" or "project" for config-backed settings
}

// SettingsOverlay is a settings menu overlay
type SettingsOverlay struct {
	twoPaneDialogChrome
	dialogViewportState
	items             []SettingItem
	cursor            int
	scroll            int
	configSource      string
	config            *config.Config
	configPath        string
	projectConfigPath string
	localConfigPath   string
	defaultSaveTarget string
	styles            *Styles
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
	menu.projectConfigPath = configSource
	menu.localConfigPath = localConfigPathFor(configSource)
	menu.defaultSaveTarget = configTargetValue(configSource)
	return menu
}

func NewSettingsOverlayWithConfigTargets(items []SettingItem, cfg *config.Config, selectedConfigPath, projectConfigPath string) *SettingsOverlay {
	menu := NewSettingsOverlayWithConfig(items, cfg, selectedConfigPath)
	menu.configPath = selectedConfigPath
	menu.configSource = selectedConfigPath
	menu.projectConfigPath = projectConfigPath
	menu.localConfigPath = localConfigPathFor(projectConfigPath)
	menu.defaultSaveTarget = configTargetValue(selectedConfigPath)
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

		case "t":
			m.toggleSaveTarget()
			return m, nil
		}
	case tea.WindowSizeMsg:
		m.ApplyWindowSize(msg)
	}

	return m, nil
}

// View renders the settings menu
func (m *SettingsOverlay) View() string {
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
			return m.renderScrollableItems(width, height)
		},
		renderRight: func(mode dialogLayoutMode, width, height int) string {
			return renderDialogActions(m.styles, []keybinds.Binding{
				{Key: "j/k", Description: "move"},
				{Key: "h/l", Description: "cycle"},
				{Key: "t", Description: "target"},
				{Key: "Enter/Space", Description: "toggle/activate"},
				{Key: "e", Description: "edit config"},
				{Key: "Esc", Description: "close"},
			})
		},
	})
}

func (m *SettingsOverlay) renderScrollableItems(width, height int) string {
	lines, selectedLine, selectedHeaderLine := m.renderLines(width)
	if len(lines) == 0 {
		return ""
	}
	visibleRows := max(1, height)
	m.ensureSelectionVisible(selectedLine, len(lines), visibleRows)
	if visibleRows > 1 && selectedHeaderLine >= 0 && selectedHeaderLine < m.scroll && selectedLine <= m.scroll {
		m.scroll = max(0, selectedLine-1)
	}
	start := m.scroll
	end := min(len(lines), start+visibleRows)
	visible := append([]string(nil), lines[start:end]...)
	if visibleRows > 1 && selectedHeaderLine >= 0 && selectedHeaderLine < start && selectedLine > start && len(visible) > 0 {
		visible[0] = lines[selectedHeaderLine]
	}
	return strings.Join(visible, "\n")
}

func (m *SettingsOverlay) renderLines(width int) ([]string, int, int) {
	lines := make([]string, 0, len(m.items)+6)
	selectedLine := 0
	selectedHeaderLine := -1
	currentLine := 0

	if strings.TrimSpace(m.configSource) != "" {
		lines = append(lines, m.styles.MenuItemDisabled.Render("Config source: "+m.configSource))
		lines = append(lines, "")
		currentLine += 2
	}

	lastGroup := ""
	currentHeaderLine := -1
	for i, item := range m.items {
		if item.Group != "" && item.Group != lastGroup {
			if len(lines) > 0 {
				lines = append(lines, "")
				currentLine++
			}
			lines = append(lines, m.styles.MenuHeader.Render(item.Group))
			currentHeaderLine = currentLine
			currentLine++
			lastGroup = item.Group
		}

		if item.Type == SettingSeparator {
			lines = append(lines, m.styles.Separator.Render(item.Label))
			currentLine++
			continue
		}

		style := m.styles.MenuItem
		if i == m.cursor {
			style = m.styles.MenuItemActive
			selectedLine = currentLine
			selectedHeaderLine = currentHeaderLine
		}

		prefix := "  "
		if i == m.cursor {
			prefix = "▶ "
		}
		label := item.Label
		if item.Group != "" {
			label = "  " + label
		}
		dots := strings.Repeat(".", max(1, 36-len([]rune(label))))

		value := ""
		switch item.Type {
		case SettingToggle:
			value = "no"
			if v, ok := item.Value.(bool); ok && v {
				value = "yes"
			}
		case SettingChoice:
			if v, ok := item.Value.(string); ok {
				value = v
			}
		case SettingAction:
			value = item.ActionHint
			if value == "" {
				value = "run"
			}
		}

		line := style.Render(prefix+label) +
			m.styles.MenuItemDisabled.Render(dots) +
			m.styles.MenuKey.Render(value)
		if item.Key != "" {
			line += " " + m.styles.MenuItemDisabled.Render("["+item.Key+"]")
		}
		if item.configBacked() {
			line += " " + m.styles.MenuItemDisabled.Render("@"+item.normalizedSaveTarget())
		}

		lines = append(lines, ansi.Truncate(line, max(8, width-4), "..."))
		currentLine++
	}

	return lines, selectedLine, selectedHeaderLine
}

func (m *SettingsOverlay) ensureSelectionVisible(selectedLine, totalLines, visibleRows int) {
	if visibleRows < 1 {
		visibleRows = 1
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
	maxScroll := max(0, totalLines-visibleRows)
	if m.scroll > maxScroll {
		m.scroll = maxScroll
	}
	if selectedLine < m.scroll {
		m.scroll = selectedLine
	}
	if selectedLine >= m.scroll+visibleRows {
		m.scroll = selectedLine - visibleRows + 1
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
	if m.scroll > maxScroll {
		m.scroll = maxScroll
	}
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
			return m.persistConfigItem(*item)
		}
		return nil

	case SettingAction:
		// Trigger action
		if item.Key == "editor" && strings.TrimSpace(m.configPath) != "" {
			return openConfigEditorMsg(m.configPath)
		}
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
		if item.Key == "editor" && strings.TrimSpace(m.configPath) != "" {
			return openConfigEditorMsg(m.configPath)
		}
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
		if key == "editor" && strings.TrimSpace(m.configPath) != "" {
			return openConfigEditorMsg(m.configPath)
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
	m.applySettingsInternalChoice(item)

	if item.OnChange != nil {
		item.OnChange(item.Value)
	}

	return m.persistConfigItem(*item)
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
	m.applySettingsInternalChoice(item)

	if item.OnChange != nil {
		item.OnChange(item.Value)
	}

	return m.persistConfigItem(*item)
}

func (m *SettingsOverlay) applySettingsInternalChoice(item *SettingItem) {
	if item == nil || item.Key != settingsDefaultSaveTargetKey {
		return
	}
	if value, ok := item.Value.(string); ok && value == "project" {
		m.defaultSaveTarget = "project"
		return
	}
	m.defaultSaveTarget = "local"
}

func (m *SettingsOverlay) toggleSaveTarget() {
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return
	}
	item := &m.items[m.cursor]
	if !item.configBacked() {
		return
	}
	switch item.normalizedSaveTarget() {
	case "default":
		item.SaveTarget = "local"
	case "local":
		item.SaveTarget = "project"
	default:
		item.SaveTarget = "default"
	}
}

func (m *SettingsOverlay) persistConfigItem(item SettingItem) tea.Cmd {
	if m.config == nil || !item.configBacked() {
		return nil
	}

	cfg := m.config
	configPath := append([]string(nil), item.ConfigPath...)
	target := m.resolvedSaveTarget(item.normalizedSaveTarget())
	targetPath := m.configPathForTarget(target)
	oppositePath := m.configPathForTarget(oppositeConfigTarget(target))
	if strings.TrimSpace(targetPath) == "" {
		return nil
	}
	return func() tea.Msg {
		if err := persistConfigField(cfg, configPath, targetPath, oppositePath); err != nil {
			return SelectionMsg{
				Key:   "settings-save-error",
				Value: err,
			}
		}
		return nil
	}
}

func (m *SettingsOverlay) resolvedSaveTarget(target string) string {
	if target == "default" {
		if m.defaultSaveTarget == "project" {
			return "project"
		}
		return "local"
	}
	return target
}

func (m *SettingsOverlay) configPathForTarget(target string) string {
	switch target {
	case "local":
		return m.localConfigPath
	case "project":
		return m.projectConfigPath
	}
	return ""
}

func configTargetValue(selectedConfigPath string) string {
	if filepath.Base(selectedConfigPath) == config.LocalConfigFileName {
		return "local"
	}
	return "project"
}

func localConfigPathFor(projectConfigPath string) string {
	if strings.TrimSpace(projectConfigPath) == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(projectConfigPath), config.LocalConfigFileName)
}

func (item SettingItem) configBacked() bool {
	return len(item.ConfigPath) > 0
}

func (item SettingItem) normalizedSaveTarget() string {
	if item.SaveTarget == "default" {
		return "default"
	}
	if item.SaveTarget == "project" {
		return "project"
	}
	return "local"
}

func oppositeConfigTarget(target string) string {
	if target == "local" {
		return "project"
	}
	return "local"
}

func configTargetForPath(projectConfigPath, localConfigPath string, jsonPath []string) string {
	if len(jsonPath) == 0 {
		return "default"
	}
	if configFileContainsPath(localConfigPath, jsonPath) {
		return "local"
	}
	if configFileContainsPath(projectConfigPath, jsonPath) {
		return "project"
	}
	return "default"
}

func configFileContainsPath(path string, jsonPath []string) bool {
	raw, err := readConfigJSON(path)
	if err != nil {
		return false
	}
	_, ok := valueAtJSONPath(raw, jsonPath)
	return ok
}

func persistConfigField(cfg *config.Config, jsonPath []string, targetPath, oppositePath string) error {
	value, ok, err := configValueAtPath(cfg, jsonPath)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("config value %s not found", strings.Join(jsonPath, "."))
	}

	targetRaw, err := readConfigJSON(targetPath)
	if err != nil {
		return fmt.Errorf("read target config: %w", err)
	}
	setJSONPath(targetRaw, jsonPath, value)
	if err := writeConfigJSON(targetPath, targetRaw); err != nil {
		return fmt.Errorf("write target config: %w", err)
	}

	if strings.TrimSpace(oppositePath) != "" && filepath.Clean(oppositePath) != filepath.Clean(targetPath) {
		oppositeRaw, err := readConfigJSON(oppositePath)
		if err != nil {
			return fmt.Errorf("read opposite config: %w", err)
		}
		if removeJSONPath(oppositeRaw, jsonPath) {
			if err := writeConfigJSON(oppositePath, oppositeRaw); err != nil {
				return fmt.Errorf("write opposite config: %w", err)
			}
		}
	}
	return nil
}

func configValueAtPath(cfg *config.Config, jsonPath []string) (any, bool, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, false, fmt.Errorf("marshal config: %w", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, false, fmt.Errorf("unmarshal config: %w", err)
	}
	value, ok := valueAtJSONPath(raw, jsonPath)
	return value, ok, nil
}

func readConfigJSON(path string) (map[string]any, error) {
	raw := map[string]any{
		"$schema":  config.ConfigSchemaURL,
		"$version": float64(config.CurrentConfigVersion),
	}
	if strings.TrimSpace(path) == "" {
		return raw, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return raw, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return raw, nil
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if version, ok := raw["$version"].(float64); ok && int(version) > config.CurrentConfigVersion {
		return nil, fmt.Errorf("unsupported config version %d in %s (max supported %d)", int(version), path, config.CurrentConfigVersion)
	}
	raw["$schema"] = config.ConfigSchemaURL
	raw["$version"] = float64(config.CurrentConfigVersion)
	return raw, nil
}

func writeConfigJSON(path string, raw map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw["$schema"] = config.ConfigSchemaURL
	raw["$version"] = config.CurrentConfigVersion
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func valueAtJSONPath(raw map[string]any, jsonPath []string) (any, bool) {
	var current any = raw
	for _, segment := range jsonPath {
		obj, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = obj[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func setJSONPath(raw map[string]any, jsonPath []string, value any) {
	current := raw
	for _, segment := range jsonPath[:len(jsonPath)-1] {
		next, ok := current[segment].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[segment] = next
		}
		current = next
	}
	current[jsonPath[len(jsonPath)-1]] = value
}

func removeJSONPath(raw map[string]any, jsonPath []string) bool {
	if len(jsonPath) == 0 {
		return false
	}
	if len(jsonPath) == 1 {
		if _, ok := raw[jsonPath[0]]; ok {
			delete(raw, jsonPath[0])
			return true
		}
		return false
	}
	next, ok := raw[jsonPath[0]].(map[string]any)
	if !ok {
		return false
	}
	removed := removeJSONPath(next, jsonPath[1:])
	if removed && len(next) == 0 {
		delete(raw, jsonPath[0])
	}
	return removed
}

func openConfigEditorMsg(configPath ...string) tea.Cmd {
	var path string
	if len(configPath) > 0 {
		path = configPath[0]
	}
	return func() tea.Msg {
		return SelectionMsg{
			Key:   "editor",
			Value: path,
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
	return NewSettingsOverlayWithEditorAndConfigTarget(editor, cfg, configSource, configSource)
}

func NewSettingsOverlayWithEditorAndConfigTarget(editor interface {
	GetShowPhases() bool
	ToggleShowPhases()
}, cfg *config.Config, selectedConfigPath, projectConfigPath string) *SettingsOverlay {
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
	orchestrationVia := "az"
	latencyTrace := false
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
		latencyTrace = cfg.Diagnostics.LatencyTrace
		if strings.TrimSpace(cfg.Orchestration.Via) != "" {
			orchestrationVia = cfg.Orchestration.Via
		}
	}
	defaultSaveTarget := configTargetValue(selectedConfigPath)
	resolvedLocalConfigPath := localConfigPathFor(projectConfigPath)
	saveTargetFor := func(jsonPath []string) string {
		return configTargetForPath(projectConfigPath, resolvedLocalConfigPath, jsonPath)
	}
	items := []SettingItem{
		{
			Key:     settingsDefaultSaveTargetKey,
			Group:   "Config",
			Label:   "Default save target",
			Type:    SettingChoice,
			Value:   defaultSaveTarget,
			Choices: []string{"local", "project"},
		},
		{
			Key:   "cli-tool",
			Group: "Session",
			Label: "CLI Tool",
			Type:  SettingChoice,
			Value: cliTool,
			ConfigPath: []string{
				"cliTool",
			},
			SaveTarget: saveTargetFor([]string{"cliTool"}),
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
			ConfigPath: []string{
				"session",
				"dangerouslySkipPermissions",
			},
			SaveTarget: saveTargetFor([]string{"session", "dangerouslySkipPermissions"}),
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
			ConfigPath: []string{
				"session",
				"timeoutMs",
			},
			SaveTarget: saveTargetFor([]string{"session", "timeoutMs"}),
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
			Key:   "orchestration-via",
			Group: "Orchestration",
			Label: "Delegation mode",
			Type:  SettingChoice,
			Value: orchestrationVia,
			ConfigPath: []string{
				"orchestration",
				"via",
			},
			SaveTarget: saveTargetFor([]string{"orchestration", "via"}),
			Choices: []string{
				"az",
				"native",
			},
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				if v, ok := value.(string); ok {
					cfg.Orchestration.Via = v
				}
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
			Key:   "latency-trace",
			Group: "Diagnostics",
			Label: "Latency trace logs",
			Type:  SettingToggle,
			Value: latencyTrace,
			ConfigPath: []string{
				"diagnostics",
				"latencyTrace",
			},
			SaveTarget: saveTargetFor([]string{"diagnostics", "latencyTrace"}),
			OnChange: func(value any) {
				if cfg == nil {
					return
				}
				if v, ok := value.(bool); ok {
					cfg.Diagnostics.LatencyTrace = v
					latencytrace.SetConfigEnabled(v)
				}
			},
		},
		{
			Key:   "git-workflow-mode",
			Group: "Git",
			Label: "Workflow mode",
			Type:  SettingChoice,
			Value: gitWorkflowMode,
			ConfigPath: []string{
				"git",
				"workflowMode",
			},
			SaveTarget: saveTargetFor([]string{"git", "workflowMode"}),
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
			ConfigPath: []string{
				"git",
				"defaultMergeStrategy",
			},
			SaveTarget: saveTargetFor([]string{"git", "defaultMergeStrategy"}),
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
			ConfigPath: []string{
				"git",
				"showLineChanges",
			},
			SaveTarget: saveTargetFor([]string{"git", "showLineChanges"}),
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
			ConfigPath: []string{
				"git",
				"pushEnabled",
			},
			SaveTarget: saveTargetFor([]string{"git", "pushEnabled"}),
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
			ConfigPath: []string{
				"git",
				"fetchEnabled",
			},
			SaveTarget: saveTargetFor([]string{"git", "fetchEnabled"}),
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
			ConfigPath: []string{
				"pr",
				"draftByDefault",
			},
			SaveTarget: saveTargetFor([]string{"pr", "draftByDefault"}),
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
			ConfigPath: []string{
				"pr",
				"autoLink",
			},
			SaveTarget: saveTargetFor([]string{"pr", "autoLink"}),
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
			ConfigPath: []string{
				"pr",
				"notifyAfterCreate",
			},
			SaveTarget: saveTargetFor([]string{"pr", "notifyAfterCreate"}),
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
			ConfigPath: []string{
				"pr",
				"createWithoutMerge",
			},
			SaveTarget: saveTargetFor([]string{"pr", "createWithoutMerge"}),
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
			ConfigPath: []string{
				"network",
				"autoDetect",
			},
			SaveTarget: saveTargetFor([]string{"network", "autoDetect"}),
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
			ConfigPath: []string{
				"network",
				"checkInterval",
			},
			SaveTarget: saveTargetFor([]string{"network", "checkInterval"}),
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
			ConfigPath: []string{
				"network",
				"offlineTimeout",
			},
			SaveTarget: saveTargetFor([]string{"network", "offlineTimeout"}),
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
			ConfigPath: []string{
				"network",
				"retryAttempts",
			},
			SaveTarget: saveTargetFor([]string{"network", "retryAttempts"}),
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
			ConfigPath: []string{
				"merge",
				"strategy",
			},
			SaveTarget: saveTargetFor([]string{"merge", "strategy"}),
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
			ConfigPath: []string{
				"merge",
				"autoMerge",
			},
			SaveTarget: saveTargetFor([]string{"merge", "autoMerge"}),
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
			ConfigPath: []string{
				"merge",
				"compareWithOrigin",
			},
			SaveTarget: saveTargetFor([]string{"merge", "compareWithOrigin"}),
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
			ConfigPath: []string{
				"notifications",
				"completedTask",
			},
			SaveTarget: saveTargetFor([]string{"notifications", "completedTask"}),
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
			ConfigPath: []string{
				"notifications",
				"failedTask",
			},
			SaveTarget: saveTargetFor([]string{"notifications", "failedTask"}),
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
			ConfigPath: []string{
				"notifications",
				"errorThreshold",
			},
			SaveTarget: saveTargetFor([]string{"notifications", "errorThreshold"}),
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
			ConfigPath: []string{
				"worktree",
				"autoCleanup",
			},
			SaveTarget: saveTargetFor([]string{"worktree", "autoCleanup"}),
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
			ConfigPath: []string{
				"worktree",
				"keepDays",
			},
			SaveTarget: saveTargetFor([]string{"worktree", "keepDays"}),
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
			ConfigPath: []string{
				"spec",
				"enabled",
			},
			SaveTarget: saveTargetFor([]string{"spec", "enabled"}),
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

	menu := NewSettingsOverlayWithConfigTargets(items, cfg, selectedConfigPath, projectConfigPath)
	menu.defaultSaveTarget = defaultSaveTarget
	return menu
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
