package overlay

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/config"
)

type mockSettingsEditor struct {
	showPhases  bool
	toggleCount int
}

func (m *mockSettingsEditor) GetShowPhases() bool {
	return m.showPhases
}

func (m *mockSettingsEditor) ToggleShowPhases() {
	m.showPhases = !m.showPhases
	m.toggleCount++
}

func settingIndexByKey(menu *SettingsOverlay, key string) int {
	for i := range menu.items {
		if menu.items[i].Key == key {
			return i
		}
	}
	return -1
}

func TestNewSettingsOverlay(t *testing.T) {
	items := []SettingItem{
		{Key: "test", Label: "Test Setting", Type: SettingToggle, Value: true},
	}

	menu := NewSettingsOverlay(items)

	if menu == nil {
		t.Fatal("expected menu to be created")
	}

	if len(menu.items) != 1 {
		t.Errorf("expected 1 item, got %d", len(menu.items))
	}
}

func TestNewDefaultSettingsOverlay(t *testing.T) {
	menu := NewDefaultSettingsOverlay()

	if menu == nil {
		t.Fatal("expected menu to be created")
	}

	if len(menu.items) == 0 {
		t.Error("expected default settings to have items")
	}

	// Should have at least one of each type
	hasToggle := false
	hasChoice := false
	hasAction := false
	for _, item := range menu.items {
		switch item.Type {
		case SettingToggle:
			hasToggle = true
		case SettingChoice:
			hasChoice = true
		case SettingAction:
			hasAction = true
		}
	}

	if !hasToggle {
		t.Error("expected default settings to have at least one toggle")
	}
	if !hasChoice {
		t.Error("expected default settings to have at least one choice")
	}
	if !hasAction {
		t.Error("expected default settings to have at least one action")
	}
}

func TestSettingsOverlay_Title(t *testing.T) {
	menu := NewDefaultSettingsOverlay()

	title := menu.Title()
	if title != "Settings" {
		t.Errorf("expected title 'Settings', got %s", title)
	}
}

func TestSettingsOverlay_Size(t *testing.T) {
	menu := NewDefaultSettingsOverlay()

	width, height := menu.Size()
	if width <= 0 {
		t.Errorf("expected positive width, got %d", width)
	}

	if height <= 0 {
		t.Errorf("expected positive height, got %d", height)
	}
}

func TestSettingsOverlay_Size_ResponsiveToWindow(t *testing.T) {
	menu := NewDefaultSettingsOverlay()
	_, _ = menu.Update(tea.WindowSizeMsg{Width: 150, Height: 60})

	width, height := menu.Size()
	if width != 120 {
		t.Fatalf("width = %d, want 120", width)
	}
	if height != 48 {
		t.Fatalf("height = %d, want 48", height)
	}
}

func TestSettingsOverlay_MoveCursor(t *testing.T) {
	items := []SettingItem{
		{Key: "1", Label: "First", Type: SettingToggle, Value: true},
		{Key: "2", Label: "Second", Type: SettingToggle, Value: false},
		{Key: "3", Label: "Third", Type: SettingToggle, Value: true},
	}

	menu := NewSettingsOverlay(items)

	// Should start at first item
	if menu.cursor != 0 {
		t.Errorf("expected cursor at 0, got %d", menu.cursor)
	}

	// Move down
	menu.moveCursorDown()
	if menu.cursor != 1 {
		t.Errorf("expected cursor at 1, got %d", menu.cursor)
	}

	// Move down again
	menu.moveCursorDown()
	if menu.cursor != 2 {
		t.Errorf("expected cursor at 2, got %d", menu.cursor)
	}

	// Move down should wrap
	menu.moveCursorDown()
	if menu.cursor != 0 {
		t.Errorf("expected cursor to wrap to 0, got %d", menu.cursor)
	}

	// Move up should wrap
	menu.moveCursorUp()
	if menu.cursor != 2 {
		t.Errorf("expected cursor to wrap to 2, got %d", menu.cursor)
	}
}

func TestSettingsOverlay_MoveCursor_SkipSeparators(t *testing.T) {
	items := []SettingItem{
		{Key: "1", Label: "First", Type: SettingToggle, Value: true},
		{Key: "", Label: "---", Type: SettingSeparator, Value: nil},
		{Key: "2", Label: "Second", Type: SettingToggle, Value: false},
	}

	menu := NewSettingsOverlay(items)

	// Should start at first item (index 0)
	if menu.cursor != 0 {
		t.Errorf("expected cursor at 0, got %d", menu.cursor)
	}

	// Move down should skip separator and go to index 2
	menu.moveCursorDown()
	if menu.cursor != 2 {
		t.Errorf("expected cursor at 2 (skipping separator), got %d", menu.cursor)
	}

	// Move up should skip separator and go back to index 0
	menu.moveCursorUp()
	if menu.cursor != 0 {
		t.Errorf("expected cursor at 0 (skipping separator), got %d", menu.cursor)
	}
}

func TestSettingsOverlay_ToggleSetting(t *testing.T) {
	changed := false
	newValue := false

	items := []SettingItem{
		{
			Key:   "test",
			Label: "Test Toggle",
			Type:  SettingToggle,
			Value: true,
			OnChange: func(value any) {
				changed = true
				if v, ok := value.(bool); ok {
					newValue = v
				}
			},
		},
	}

	menu := NewSettingsOverlay(items)

	// Toggle the setting
	menu.toggleOrActivate()

	if !changed {
		t.Error("expected OnChange to be called")
	}

	if newValue != false {
		t.Errorf("expected value to be toggled to false, got %v", newValue)
	}

	if v, ok := menu.items[0].Value.(bool); !ok || v != false {
		t.Errorf("expected item value to be false, got %v", menu.items[0].Value)
	}
}

func TestSettingsOverlay_IncrementChoice(t *testing.T) {
	changed := false
	newValue := ""

	items := []SettingItem{
		{
			Key:     "theme",
			Label:   "Theme",
			Type:    SettingChoice,
			Value:   "first",
			Choices: []string{"first", "second", "third"},
			OnChange: func(value any) {
				changed = true
				if v, ok := value.(string); ok {
					newValue = v
				}
			},
		},
	}

	menu := NewSettingsOverlay(items)

	// Increment choice
	menu.incrementChoice()

	if !changed {
		t.Error("expected OnChange to be called")
	}

	if newValue != "second" {
		t.Errorf("expected value to be 'second', got %s", newValue)
	}

	// Increment again
	menu.incrementChoice()
	if v, ok := menu.items[0].Value.(string); !ok || v != "third" {
		t.Errorf("expected item value to be 'third', got %v", menu.items[0].Value)
	}

	// Increment should wrap
	menu.incrementChoice()
	if v, ok := menu.items[0].Value.(string); !ok || v != "first" {
		t.Errorf("expected item value to wrap to 'first', got %v", menu.items[0].Value)
	}
}

func TestSettingsOverlay_DecrementChoice(t *testing.T) {
	items := []SettingItem{
		{
			Key:     "theme",
			Label:   "Theme",
			Type:    SettingChoice,
			Value:   "second",
			Choices: []string{"first", "second", "third"},
		},
	}

	menu := NewSettingsOverlay(items)

	// Decrement choice
	menu.decrementChoice()
	if v, ok := menu.items[0].Value.(string); !ok || v != "first" {
		t.Errorf("expected item value to be 'first', got %v", menu.items[0].Value)
	}

	// Decrement should wrap
	menu.decrementChoice()
	if v, ok := menu.items[0].Value.(string); !ok || v != "third" {
		t.Errorf("expected item value to wrap to 'third', got %v", menu.items[0].Value)
	}
}

func TestSettingsOverlay_ActionSetting(t *testing.T) {
	actionCalled := false

	items := []SettingItem{
		{
			Key:   "action",
			Label: "Test Action",
			Type:  SettingAction,
			OnAction: func() tea.Cmd {
				actionCalled = true
				return nil
			},
		},
	}

	menu := NewSettingsOverlay(items)

	// Activate action
	menu.toggleOrActivate()

	if !actionCalled {
		t.Error("expected OnAction to be called")
	}
}

func TestSettingsOverlay_EditKeyRequestsAppEditor(t *testing.T) {
	menu := NewDefaultSettingsOverlay()

	_, cmd := menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if cmd == nil {
		t.Fatal("expected editor command")
	}

	rawMsg := cmd()
	msg, ok := rawMsg.(SelectionMsg)
	if !ok {
		t.Fatalf("expected SelectionMsg, got %T", rawMsg)
	}
	if msg.Key != "editor" {
		t.Fatalf("selection key = %q, want editor", msg.Key)
	}
}

func TestSettingsOverlay_KeyboardNavigation(t *testing.T) {
	items := []SettingItem{
		{Key: "1", Label: "First", Type: SettingToggle, Value: true},
		{Key: "2", Label: "Second", Type: SettingToggle, Value: false},
	}

	menu := NewSettingsOverlay(items)

	tests := []struct {
		name           string
		key            string
		expectedCursor int
	}{
		{"down arrow", "down", 1},
		{"j key", "j", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset cursor
			menu.cursor = 0

			msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tt.key)}
			if tt.key == "down" {
				msg = tea.KeyMsg{Type: tea.KeyDown}
			}

			menu.Update(msg)

			if menu.cursor != tt.expectedCursor {
				t.Errorf("expected cursor at %d, got %d", tt.expectedCursor, menu.cursor)
			}
		})
	}
}

func TestSettingsOverlay_EscapeKey(t *testing.T) {
	menu := NewDefaultSettingsOverlay()

	msg := tea.KeyMsg{Type: tea.KeyEscape}
	_, cmd := menu.Update(msg)

	if cmd == nil {
		t.Fatal("expected command to be returned")
	}

	// Execute command and check for CloseOverlayMsg
	result := cmd()
	if _, ok := result.(CloseOverlayMsg); !ok {
		t.Errorf("expected CloseOverlayMsg, got %T", result)
	}
}

func TestSettingsOverlay_SpaceToToggle(t *testing.T) {
	items := []SettingItem{
		{Key: "test", Label: "Test", Type: SettingToggle, Value: true},
	}

	menu := NewSettingsOverlay(items)

	// Press space
	msg := tea.KeyMsg{Type: tea.KeySpace}
	menu.Update(msg)

	// Value should be toggled
	if v, ok := menu.items[0].Value.(bool); !ok || v != false {
		t.Errorf("expected value to be toggled to false, got %v", menu.items[0].Value)
	}
}

func TestSettingsOverlay_LeftRightForChoice(t *testing.T) {
	items := []SettingItem{
		{
			Key:     "theme",
			Label:   "Theme",
			Type:    SettingChoice,
			Value:   "second",
			Choices: []string{"first", "second", "third"},
		},
	}

	menu := NewSettingsOverlay(items)

	// Press right (or 'l')
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")}
	menu.Update(msg)

	if v, ok := menu.items[0].Value.(string); !ok || v != "third" {
		t.Errorf("expected value to be 'third', got %v", menu.items[0].Value)
	}

	// Press left (or 'h')
	msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")}
	menu.Update(msg)

	if v, ok := menu.items[0].Value.(string); !ok || v != "second" {
		t.Errorf("expected value to be 'second', got %v", menu.items[0].Value)
	}
}

func TestSettingsOverlay_View(t *testing.T) {
	items := []SettingItem{
		{Key: "toggle", Label: "Toggle Setting", Type: SettingToggle, Value: true},
		{Key: "choice", Label: "Choice Setting", Type: SettingChoice, Value: "option1", Choices: []string{"option1", "option2"}},
		{Key: "", Label: "---", Type: SettingSeparator},
		{Key: "action", Label: "Action Setting", Type: SettingAction},
	}

	menu := NewSettingsOverlay(items)

	view := menu.View()

	if view == "" {
		t.Error("expected non-empty view")
	}

	// Should contain labels
	if !contains(view, "Toggle Setting") {
		t.Error("expected view to contain 'Toggle Setting'")
	}

	if !contains(view, "Choice Setting") {
		t.Error("expected view to contain 'Choice Setting'")
	}

	if !contains(view, "Action Setting") {
		t.Error("expected view to contain 'Action Setting'")
	}

	// Should contain separator
	if !contains(view, "---") {
		t.Error("expected view to contain separator")
	}
}

func TestSettingsOverlay_View_ScrollsToKeepCursorVisible(t *testing.T) {
	items := make([]SettingItem, 0, 30)
	for i := 1; i <= 30; i++ {
		items = append(items, SettingItem{
			Key:   "setting-" + strconv.Itoa(i),
			Group: "General",
			Label: "Setting " + strconv.Itoa(i),
			Type:  SettingToggle,
			Value: false,
		})
	}
	menu := NewSettingsOverlay(items)
	menu.Update(tea.WindowSizeMsg{Width: 72, Height: 22})

	for i := 0; i < len(items)-1; i++ {
		menu.moveCursorDown()
	}

	view := menu.View()
	if !contains(view, "Setting 30") {
		t.Fatalf("expected scrolled view to include selected bottom row, view=%q", view)
	}
	if !contains(view, "General") {
		t.Fatalf("expected scrolled view to keep active group header visible, view=%q", view)
	}
	if menu.scroll <= 0 {
		t.Fatalf("expected positive scroll offset for bottom selection, got %d", menu.scroll)
	}
}

func TestSettingsOverlay_ConfigBackedSessionSettingsPersist(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.CLITool = "claude"
	cfg.Session.DangerouslySkipPermissions = false

	menu := NewSettingsOverlayWithEditorAndConfig(&mockSettingsEditor{showPhases: true}, cfg, filepath.Join(tmpDir, config.ConfigDirName, config.ConfigFileName))

	cliToolIdx := settingIndexByKey(menu, "cli-tool")
	if cliToolIdx < 0 {
		t.Fatal("setting \"cli-tool\" not found")
	}
	skipPermissionsIdx := settingIndexByKey(menu, "skip-permissions")
	if skipPermissionsIdx < 0 {
		t.Fatal("setting \"skip-permissions\" not found")
	}

	if got := menu.items[cliToolIdx].Value; got != "claude" {
		t.Fatalf("cli tool value = %v, want claude", got)
	}
	if got := menu.items[skipPermissionsIdx].Value; got != false {
		t.Fatalf("skip permissions value = %v, want false", got)
	}

	menu.cursor = cliToolIdx
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")}
	_, cmd := menu.Update(msg)
	if cmd == nil {
		t.Fatal("expected config persistence command after CLI tool change")
	}
	if result := cmd(); result != nil {
		t.Fatalf("expected nil result from successful config save, got %T", result)
	}

	menu.cursor = skipPermissionsIdx
	msg = tea.KeyMsg{Type: tea.KeySpace}
	_, cmd = menu.Update(msg)
	if cmd == nil {
		t.Fatal("expected config persistence command after permissions toggle")
	}
	if result := cmd(); result != nil {
		t.Fatalf("expected nil result from successful config save, got %T", result)
	}

	loaded, err := config.LoadConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if loaded.CLITool != "opencode" {
		t.Fatalf("loaded cli tool = %q, want opencode", loaded.CLITool)
	}
	if !loaded.Session.DangerouslySkipPermissions {
		t.Fatal("expected loaded permissions setting to be true")
	}
}

func TestSettingsOverlay_ConfigBackedLocalSaveTargetPersistsToLocalConfig(t *testing.T) {
	tmpDir := t.TempDir()
	projectPath := filepath.Join(tmpDir, config.ConfigDirName, config.ConfigFileName)
	localPath := filepath.Join(tmpDir, config.ConfigDirName, config.LocalConfigFileName)
	cfg := config.DefaultConfig()
	cfg.Orchestration.Via = "az"

	menu := NewSettingsOverlayWithEditorAndConfigTarget(&mockSettingsEditor{showPhases: true}, cfg, localPath, projectPath)

	orchIdx := settingIndexByKey(menu, "orchestration-via")
	if orchIdx < 0 {
		t.Fatal("setting \"orchestration-via\" not found")
	}
	defaultTargetIdx := settingIndexByKey(menu, settingsDefaultSaveTargetKey)
	if defaultTargetIdx < 0 {
		t.Fatal("setting \"settings-default-save-target\" not found")
	}
	if got := menu.items[defaultTargetIdx].Value; got != "local" {
		t.Fatalf("default save target = %q, want local", got)
	}
	if got := menu.items[orchIdx].SaveTarget; got != "default" {
		t.Fatalf("orchestration save target = %q, want default", got)
	}
	_, _ = menu.Update(tea.WindowSizeMsg{Width: 150, Height: 60})
	if view := menu.View(); !contains(view, "@default") {
		t.Fatalf("expected settings view to show per-setting default target, view=%q", view)
	}

	menu.cursor = orchIdx
	_, cmd := menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if cmd == nil {
		t.Fatal("expected config save command after cycling orchestration-via")
	}
	if result := cmd(); result != nil {
		t.Fatalf("expected nil result from successful config save, got %T", result)
	}

	if _, err := os.Stat(projectPath); !os.IsNotExist(err) {
		t.Fatalf("project config should not be written, stat err = %v", err)
	}
	loaded, err := config.LoadConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if loaded.Orchestration.Via != "native" {
		t.Fatalf("orchestration via = %q, want native", loaded.Orchestration.Via)
	}
	if _, err := os.Stat(localPath); err != nil {
		t.Fatalf("expected local config to be written: %v", err)
	}

	menu.cursor = defaultTargetIdx
	_, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if got := menu.defaultSaveTarget; got != "project" {
		t.Fatalf("default save target = %q, want project", got)
	}
	menu.cursor = orchIdx
	_, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if got := menu.items[orchIdx].SaveTarget; got != "local" {
		t.Fatalf("orchestration save target = %q, want local", got)
	}
	_, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if got := menu.items[orchIdx].SaveTarget; got != "project" {
		t.Fatalf("orchestration save target = %q, want project", got)
	}
	_, cmd = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if cmd == nil {
		t.Fatal("expected config save command after cycling orchestration-via with project target")
	}
	if result := cmd(); result != nil {
		t.Fatalf("expected nil result from successful project config save, got %T", result)
	}

	projectRaw, err := readConfigJSON(projectPath)
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	if got, ok := valueAtJSONPath(projectRaw, []string{"orchestration", "via"}); !ok || got != "az" {
		t.Fatalf("project orchestration via = %v (present=%v), want az", got, ok)
	}
	localRaw, err := readConfigJSON(localPath)
	if err != nil {
		t.Fatalf("read local config: %v", err)
	}
	if got, ok := valueAtJSONPath(localRaw, []string{"orchestration", "via"}); ok {
		t.Fatalf("local orchestration via should be removed after project save, got %v", got)
	}
}

func TestSettingsOverlay_ConfigBackedGitPrAndNetworkSettingsPersist(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Git.PushEnabled = true
	cfg.Git.FetchEnabled = true
	cfg.PR.DraftByDefault = true
	cfg.PR.AutoLink = true
	cfg.PR.NotifyAfterCreate = true
	cfg.PR.CreateWithoutMerge = false
	cfg.Network.AutoDetect = true

	menu := NewSettingsOverlayWithEditorAndConfig(
		&mockSettingsEditor{showPhases: true},
		cfg,
		filepath.Join(tmpDir, config.ConfigDirName, config.ConfigFileName),
	)

	toggles := []struct {
		key      string
		expected bool
	}{
		{key: "git-push-enabled", expected: false},
		{key: "git-fetch-enabled", expected: false},
		{key: "pr-draft-by-default", expected: false},
		{key: "pr-auto-link", expected: false},
		{key: "pr-notify-after-create", expected: false},
		{key: "pr-create-without-merge", expected: true},
		{key: "network-auto-detect", expected: false},
	}

	for _, tt := range toggles {
		idx := settingIndexByKey(menu, tt.key)
		if idx < 0 {
			t.Fatalf("setting %q not found", tt.key)
		}

		menu.cursor = idx
		_, cmd := menu.Update(tea.KeyMsg{Type: tea.KeySpace})
		if cmd == nil {
			t.Fatalf("expected config save command after toggling %q", tt.key)
		}
		if result := cmd(); result != nil {
			t.Fatalf("expected nil result from successful config save for %q, got %T", tt.key, result)
		}
	}

	loaded, err := config.LoadConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	if loaded.Git.PushEnabled {
		t.Fatal("expected git push to be disabled after toggle")
	}
	if loaded.Git.FetchEnabled {
		t.Fatal("expected git fetch to be disabled after toggle")
	}
	if loaded.PR.DraftByDefault {
		t.Fatal("expected draft-by-default to be disabled after toggle")
	}
	if loaded.PR.AutoLink {
		t.Fatal("expected auto-link to be disabled after toggle")
	}
	if loaded.PR.NotifyAfterCreate {
		t.Fatal("expected notify-after-create to be disabled after toggle")
	}
	if !loaded.PR.CreateWithoutMerge {
		t.Fatal("expected create-without-merge to be enabled after toggle")
	}
	if loaded.Network.AutoDetect {
		t.Fatal("expected network auto-detect to be disabled after toggle")
	}
}

func TestSettingsOverlay_ConfigBackedExpandedSettingsPersist(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Git.WorkflowMode = "worktree"
	cfg.Git.DefaultMergeStrategy = "merge"
	cfg.Network.CheckInterval = 60
	cfg.Merge.Strategy = "merge"
	cfg.Notifications.ErrorThreshold = 3
	cfg.Worktree.KeepDays = 7
	cfg.Spec.Enabled = true
	cfg.Orchestration.Via = "az"

	menu := NewSettingsOverlayWithEditorAndConfig(
		&mockSettingsEditor{showPhases: true},
		cfg,
		filepath.Join(tmpDir, config.ConfigDirName, config.ConfigFileName),
	)

	for _, key := range []string{
		"git-workflow-mode",
		"git-default-merge-strategy",
		"network-check-interval",
		"merge-strategy",
		"notifications-error-threshold",
		"worktree-keep-days",
		"orchestration-via",
	} {
		idx := settingIndexByKey(menu, key)
		if idx < 0 {
			t.Fatalf("setting %q not found", key)
		}
		menu.cursor = idx
		_, cmd := menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
		if cmd == nil {
			t.Fatalf("expected config save command after cycling %q", key)
		}
		if result := cmd(); result != nil {
			t.Fatalf("expected nil result from successful config save for %q, got %T", key, result)
		}
	}

	specIdx := settingIndexByKey(menu, "spec-enabled")
	if specIdx < 0 {
		t.Fatal("setting \"spec-enabled\" not found")
	}
	menu.cursor = specIdx
	_, cmd := menu.Update(tea.KeyMsg{Type: tea.KeySpace})
	if cmd == nil {
		t.Fatal("expected config save command after toggling spec-enabled")
	}
	if result := cmd(); result != nil {
		t.Fatalf("expected nil result from successful config save for spec-enabled, got %T", result)
	}

	loaded, err := config.LoadConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	if loaded.Git.WorkflowMode != "branch" {
		t.Fatalf("git workflow mode = %q, want branch", loaded.Git.WorkflowMode)
	}
	if loaded.Git.DefaultMergeStrategy != "squash" {
		t.Fatalf("git default merge strategy = %q, want squash", loaded.Git.DefaultMergeStrategy)
	}
	if loaded.Network.CheckInterval != 120 {
		t.Fatalf("network check interval = %d, want 120", loaded.Network.CheckInterval)
	}
	if loaded.Merge.Strategy != "squash" {
		t.Fatalf("merge strategy = %q, want squash", loaded.Merge.Strategy)
	}
	if loaded.Notifications.ErrorThreshold != 5 {
		t.Fatalf("notifications error threshold = %d, want 5", loaded.Notifications.ErrorThreshold)
	}
	if loaded.Worktree.KeepDays != 14 {
		t.Fatalf("worktree keep days = %d, want 14", loaded.Worktree.KeepDays)
	}
	if loaded.Spec.Enabled {
		t.Fatal("expected spec-enabled to be toggled to false")
	}
	if loaded.Orchestration.Via != "native" {
		t.Fatalf("orchestration via = %q, want native", loaded.Orchestration.Via)
	}
}

// Helper function to check if string contains substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && containsAt(s, substr))
}

func containsAt(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
