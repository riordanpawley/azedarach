package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

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
