package keybinds

import (
	"strings"

	"github.com/riordanpawley/azedarach/internal/types"
)

type ActionID string

type KeySpec struct {
	Input   string
	Display string
}

type ActionSpec struct {
	ID       ActionID
	Mode     types.Mode
	Category string
	Keys     []KeySpec
	Hint     string
	HintKey  string
	Help     string
	HelpKey  string
}

const (
	ActionQuit ActionID = "quit"

	ActionMoveUp       ActionID = "move_up"
	ActionMoveDown     ActionID = "move_down"
	ActionMoveLeft     ActionID = "move_left"
	ActionMoveRight    ActionID = "move_right"
	ActionHalfPageUp   ActionID = "half_page_up"
	ActionHalfPageDown ActionID = "half_page_down"

	ActionEnterGoto      ActionID = "enter_goto"
	ActionEnterSearch    ActionID = "enter_search"
	ActionOpenFilter     ActionID = "open_filter"
	ActionOpenSort       ActionID = "open_sort"
	ActionEnterSelect    ActionID = "enter_select"
	ActionOpenHelp       ActionID = "open_help"
	ActionOpenWorkspace  ActionID = "open_workspace"
	ActionDrillDown      ActionID = "drill_down"
	ActionCreateTask     ActionID = "create_task"
	ActionOpenSettings   ActionID = "open_settings"
	ActionOpenDiagnostic ActionID = "open_diagnostics"
	ActionOpenRecovery   ActionID = "open_recovery"
	ActionToggleView     ActionID = "toggle_view"
	ActionRefresh        ActionID = "refresh"
	ActionAttachSession  ActionID = "attach_session"

	ActionSelectToggle     ActionID = "select_toggle"
	ActionSelectColumnAll  ActionID = "select_column_all"
	ActionSelectAllVisible ActionID = "select_all_visible"
	ActionSelectInvert     ActionID = "select_invert"
	ActionSelectClear      ActionID = "select_clear"
	ActionSelectBulk       ActionID = "select_bulk"
	ActionSelectExit       ActionID = "select_exit"

	ActionGotoTop      ActionID = "goto_top"
	ActionGotoBottom   ActionID = "goto_bottom"
	ActionGotoFirstCol ActionID = "goto_first_col"
	ActionGotoLastCol  ActionID = "goto_last_col"
	ActionGotoJump     ActionID = "goto_jump"
	ActionGotoProjects ActionID = "goto_projects"
	ActionGotoSpec     ActionID = "goto_spec"
)

var registry = []ActionSpec{
	// Normal mode lookup + status hints.
	{ID: ActionMoveUp, Mode: types.ModeNormal, Keys: []KeySpec{{Input: "k", Display: "k"}, {Input: "up", Display: "up"}}},
	{ID: ActionMoveDown, Mode: types.ModeNormal, Keys: []KeySpec{{Input: "j", Display: "j"}, {Input: "down", Display: "down"}}},
	{ID: ActionMoveLeft, Mode: types.ModeNormal, Keys: []KeySpec{{Input: "h", Display: "h"}, {Input: "left", Display: "left"}}},
	{ID: ActionMoveRight, Mode: types.ModeNormal, Keys: []KeySpec{{Input: "l", Display: "l"}, {Input: "right", Display: "right"}}},
	{ID: ActionHalfPageUp, Mode: types.ModeNormal, Keys: []KeySpec{{Input: "ctrl+u", Display: "ctrl+u"}}},
	{ID: ActionHalfPageDown, Mode: types.ModeNormal, Keys: []KeySpec{{Input: "ctrl+d", Display: "ctrl+d"}}},
	{ID: ActionOpenWorkspace, Mode: types.ModeNormal, Keys: []KeySpec{{Input: " ", Display: "Space"}}, Hint: "task workspace"},
	{ID: ActionEnterGoto, Mode: types.ModeNormal, Keys: []KeySpec{{Input: "g", Display: "g"}}, Hint: "goto"},
	{ID: ActionEnterSearch, Mode: types.ModeNormal, Keys: []KeySpec{{Input: "/", Display: "/"}}, Hint: "search"},
	{ID: ActionOpenFilter, Mode: types.ModeNormal, Keys: []KeySpec{{Input: "f", Display: "f"}}, Hint: "filter"},
	{ID: ActionOpenSort, Mode: types.ModeNormal, Keys: []KeySpec{{Input: ",", Display: ","}}, Hint: "sort"},
	{ID: ActionEnterSelect, Mode: types.ModeNormal, Keys: []KeySpec{{Input: "v", Display: "v"}}, Hint: "select"},
	{ID: ActionDrillDown, Mode: types.ModeNormal, Keys: []KeySpec{{Input: "enter", Display: "Enter"}}, Hint: "drill"},
	{ID: ActionAttachSession, Mode: types.ModeNormal, Keys: []KeySpec{{Input: "a", Display: "a"}}, Hint: "attach"},
	{ID: ActionCreateTask, Mode: types.ModeNormal, Keys: []KeySpec{{Input: "c", Display: "c"}}, Hint: "create"},
	{ID: ActionOpenSettings, Mode: types.ModeNormal, Keys: []KeySpec{{Input: "s", Display: "s"}}, Hint: "settings"},
	{ID: ActionRefresh, Mode: types.ModeNormal, Keys: []KeySpec{{Input: "r", Display: "r"}}, Hint: "refresh"},
	{ID: ActionOpenDiagnostic, Mode: types.ModeNormal, Keys: []KeySpec{{Input: "D", Display: "D"}}},
	{ID: ActionOpenRecovery, Mode: types.ModeNormal, Keys: []KeySpec{{Input: "n", Display: "n"}}, Hint: "recover"},
	{ID: ActionToggleView, Mode: types.ModeNormal, Keys: []KeySpec{{Input: "tab", Display: "Tab"}}, Hint: "view"},
	{ID: ActionOpenHelp, Mode: types.ModeNormal, Keys: []KeySpec{{Input: "?", Display: "?"}}, Hint: "help"},
	{ID: ActionQuit, Mode: types.ModeNormal, Keys: []KeySpec{{Input: "q", Display: "q"}}, Hint: "quit"},

	// Select mode lookup + status hints.
	{ID: ActionMoveUp, Mode: types.ModeSelect, Keys: []KeySpec{{Input: "k", Display: "k"}, {Input: "up", Display: "up"}}},
	{ID: ActionMoveDown, Mode: types.ModeSelect, Keys: []KeySpec{{Input: "j", Display: "j"}, {Input: "down", Display: "down"}}},
	{ID: ActionMoveLeft, Mode: types.ModeSelect, Keys: []KeySpec{{Input: "h", Display: "h"}, {Input: "left", Display: "left"}}},
	{ID: ActionMoveRight, Mode: types.ModeSelect, Keys: []KeySpec{{Input: "l", Display: "l"}, {Input: "right", Display: "right"}}},
	{ID: ActionHalfPageUp, Mode: types.ModeSelect, Keys: []KeySpec{{Input: "ctrl+u", Display: "ctrl+u"}}},
	{ID: ActionHalfPageDown, Mode: types.ModeSelect, Keys: []KeySpec{{Input: "ctrl+d", Display: "ctrl+d"}}},
	{ID: ActionSelectToggle, Mode: types.ModeSelect, Keys: []KeySpec{{Input: "a", Display: "a"}, {Input: "5", Display: "5"}}, Hint: "toggle", HintKey: "a/5"},
	{ID: ActionSelectColumnAll, Mode: types.ModeSelect, Keys: []KeySpec{{Input: "A", Display: "A"}}, Hint: "column"},
	{ID: ActionSelectAllVisible, Mode: types.ModeSelect, Keys: []KeySpec{{Input: "%", Display: "%"}}, Hint: "all"},
	{ID: ActionSelectInvert, Mode: types.ModeSelect, Keys: []KeySpec{{Input: "*", Display: "*"}}, Hint: "invert"},
	{ID: ActionSelectClear, Mode: types.ModeSelect, Keys: []KeySpec{{Input: "x", Display: "x"}}, Hint: "clear"},
	{ID: ActionSelectBulk, Mode: types.ModeSelect, Keys: []KeySpec{{Input: " ", Display: "Space"}, {Input: "enter", Display: "Enter"}}, Hint: "bulk", HintKey: "Space/Enter"},
	{ID: ActionSelectExit, Mode: types.ModeSelect, Keys: []KeySpec{{Input: "v", Display: "v"}, {Input: "esc", Display: "Esc"}}, Hint: "exit", HintKey: "v/Esc"},

	// Search mode status hints.
	{Mode: types.ModeSearch, Keys: []KeySpec{{Display: "Type"}}, Hint: "search", HintKey: "Type"},
	{Mode: types.ModeSearch, Keys: []KeySpec{{Input: "enter", Display: "Enter"}}, Hint: "confirm"},
	{Mode: types.ModeSearch, Keys: []KeySpec{{Input: "esc", Display: "Esc"}}, Hint: "cancel"},

	// Goto mode lookup + status hints.
	{ID: ActionGotoTop, Mode: types.ModeGoto, Keys: []KeySpec{{Input: "g", Display: "g"}}, Hint: "top", HintKey: "g g"},
	{ID: ActionGotoBottom, Mode: types.ModeGoto, Keys: []KeySpec{{Input: "e", Display: "e"}}, Hint: "bottom", HintKey: "g e"},
	{ID: ActionGotoFirstCol, Mode: types.ModeGoto, Keys: []KeySpec{{Input: "h", Display: "h"}}, Hint: "first col", HintKey: "g h"},
	{ID: ActionGotoLastCol, Mode: types.ModeGoto, Keys: []KeySpec{{Input: "l", Display: "l"}}, Hint: "last col", HintKey: "g l"},
	{ID: ActionGotoJump, Mode: types.ModeGoto, Keys: []KeySpec{{Input: "w", Display: "w"}}, Hint: "labels", HintKey: "g w"},
	{ID: ActionGotoProjects, Mode: types.ModeGoto, Keys: []KeySpec{{Input: "p", Display: "p"}}, Hint: "projects", HintKey: "g p"},
	{ID: ActionGotoSpec, Mode: types.ModeGoto, Keys: []KeySpec{{Input: "s", Display: "s"}}, Hint: "spec", HintKey: "g s"},
	{Mode: types.ModeGoto, Keys: []KeySpec{{Input: "esc", Display: "Esc"}}, Hint: "cancel"},

	// Action mode status hints.
	{Mode: types.ModeAction, Keys: []KeySpec{{Display: "h/l"}}, Hint: "move"},
	{Mode: types.ModeAction, Keys: []KeySpec{{Display: "s/S/!"}}, Hint: "start"},
	{Mode: types.ModeAction, Keys: []KeySpec{{Display: "a"}}, Hint: "attach"},
	{Mode: types.ModeAction, Keys: []KeySpec{{Display: "p"}}, Hint: "pause"},
	{Mode: types.ModeAction, Keys: []KeySpec{{Display: "R"}}, Hint: "resume"},
	{Mode: types.ModeAction, Keys: []KeySpec{{Display: "r"}}, Hint: "refresh"},
	{Mode: types.ModeAction, Keys: []KeySpec{{Display: "V"}}, Hint: "dev"},
	{Mode: types.ModeAction, Keys: []KeySpec{{Display: "x"}}, Hint: "stop"},
	{Mode: types.ModeAction, Keys: []KeySpec{{Display: "u"}}, Hint: "update"},
	{Mode: types.ModeAction, Keys: []KeySpec{{Display: "m/b"}}, Hint: "merge"},
	{Mode: types.ModeAction, Keys: []KeySpec{{Display: "P/O"}}, Hint: "PR"},
	{Mode: types.ModeAction, Keys: []KeySpec{{Display: "M"}}, Hint: "abort"},
	{Mode: types.ModeAction, Keys: []KeySpec{{Display: "H"}}, Hint: "helix"},
	{Mode: types.ModeAction, Keys: []KeySpec{{Display: "i"}}, Hint: "attachments"},
	{Mode: types.ModeAction, Keys: []KeySpec{{Display: "f"}}, Hint: "diff"},
	{Mode: types.ModeAction, Keys: []KeySpec{{Display: "w/W"}}, Hint: "cleanup"},
	{Mode: types.ModeAction, Keys: []KeySpec{{Display: "e"}}, Hint: "edit"},
	{Mode: types.ModeAction, Keys: []KeySpec{{Display: "c"}}, Hint: "child"},
	{Mode: types.ModeAction, Keys: []KeySpec{{Display: "T/d"}}, Hint: "tombstone/delete"},
	{Mode: types.ModeAction, Keys: []KeySpec{{Display: "Esc/q"}}, Hint: "cancel"},

	// Help catalog (canonical board-focused reference).
	{Mode: types.ModeNormal, Category: "Navigation", HelpKey: "h/l", Help: "Move between columns"},
	{Mode: types.ModeNormal, Category: "Navigation", HelpKey: "j/k", Help: "Move up/down in column"},
	{Mode: types.ModeNormal, Category: "Navigation", HelpKey: "ctrl+u / ctrl+d", Help: "Half-page scroll"},
	{Mode: types.ModeNormal, Category: "Navigation", HelpKey: "g then g/e/h/l", Help: "Jump in board"},
	{Mode: types.ModeNormal, Category: "Navigation", HelpKey: "g then w/p/s", Help: "Jump / projects / spec"},
	{Mode: types.ModeNormal, Category: "Navigation", HelpKey: "gg/ge", Help: "Jump to top/bottom of column"},
	{Mode: types.ModeNormal, Category: "Navigation", HelpKey: "gh/gl", Help: "Jump to first/last column"},

	{Mode: types.ModeNormal, Category: "Workspace", HelpKey: "Space", Help: "Open task workspace (details + actions)"},
	{Mode: types.ModeNormal, Category: "Workspace", HelpKey: "Enter", Help: "Drill into epic children"},

	{Mode: types.ModeNormal, Category: "Modes", HelpKey: "g", Help: "Goto mode"},
	{Mode: types.ModeNormal, Category: "Modes", HelpKey: "/", Help: "Search"},
	{Mode: types.ModeNormal, Category: "Modes", HelpKey: "f", Help: "Filter menu"},
	{Mode: types.ModeNormal, Category: "Modes", HelpKey: ",", Help: "Sort menu"},
	{Mode: types.ModeNormal, Category: "Modes", HelpKey: "v", Help: "Select mode"},
	{Mode: types.ModeNormal, Category: "Modes", HelpKey: "?", Help: "Help (this screen)"},

	{Mode: types.ModeNormal, Category: "Selection", HelpKey: "v then a/5", Help: "Toggle selection on current task"},
	{Mode: types.ModeNormal, Category: "Selection", HelpKey: "v then A", Help: "Select all in current column"},
	{Mode: types.ModeNormal, Category: "Selection", HelpKey: "v then %", Help: "Select all visible tasks"},
	{Mode: types.ModeNormal, Category: "Selection", HelpKey: "v then *", Help: "Invert visible selection"},
	{Mode: types.ModeNormal, Category: "Selection", HelpKey: "v then x", Help: "Clear selection"},
	{Mode: types.ModeNormal, Category: "Selection", HelpKey: "v then Space/Enter", Help: "Open bulk actions for selected tasks"},
	{Mode: types.ModeNormal, Category: "Selection", HelpKey: "v/Esc", Help: "Exit select mode"},

	{Mode: types.ModeNormal, Category: "Task Actions", HelpKey: "a", Help: "Attach to selected issue session"},
	{Mode: types.ModeNormal, Category: "Task Actions", HelpKey: "r (board)", Help: "Refresh board data"},
	{Mode: types.ModeNormal, Category: "Task Actions", HelpKey: "i", Help: "Open attachment manager in workspace"},
	{Mode: types.ModeNormal, Category: "Task Actions", HelpKey: "b", Help: "Open merge-into selector in workspace"},
	{Mode: types.ModeNormal, Category: "Task Actions", HelpKey: "r (workspace)", Help: "Refresh issue in workspace"},
	{Mode: types.ModeNormal, Category: "Task Actions", HelpKey: "V (workspace)", Help: "Open dev server menu"},
	{Mode: types.ModeNormal, Category: "Task Actions", HelpKey: "w/W", Help: "Cleanup worktree / delete + cleanup"},
	{Mode: types.ModeNormal, Category: "Task Actions", HelpKey: "n", Help: "Open async failure recovery overlay"},

	{Mode: types.ModeNormal, Category: "Other", HelpKey: "Tab", Help: "Toggle compact/kanban view"},
	{Mode: types.ModeNormal, Category: "Other", HelpKey: "esc", Help: "Close overlay / exit mode"},
	{Mode: types.ModeNormal, Category: "Other", HelpKey: "ctrl+g", Help: "Close all stacked overlays"},
	{Mode: types.ModeNormal, Category: "Other", HelpKey: "q", Help: "Quit"},
}

func LookupAction(mode types.Mode, input string) (ActionID, bool) {
	for _, spec := range registry {
		if spec.Mode != mode {
			continue
		}
		for _, key := range spec.Keys {
			if key.Input == "" {
				continue
			}
			if key.Input == input {
				return spec.ID, true
			}
		}
	}
	return "", false
}

func HintBindings(mode types.Mode) []Binding {
	specs := specsForMode(mode)
	out := make([]Binding, 0, len(specs))
	for _, spec := range specs {
		hint := strings.TrimSpace(spec.Hint)
		if hint == "" {
			continue
		}
		key := strings.TrimSpace(spec.HintKey)
		if key == "" {
			key = joinDisplays(spec.Keys)
		}
		if key == "" {
			continue
		}
		out = append(out, Binding{Key: key, Description: hint})
	}
	return out
}

func HelpCategories() []Category {
	ordered := make([]string, 0, 8)
	byCategory := map[string][]Binding{}
	for _, spec := range registry {
		category := strings.TrimSpace(spec.Category)
		help := strings.TrimSpace(spec.Help)
		if category == "" || help == "" {
			continue
		}
		key := strings.TrimSpace(spec.HelpKey)
		if key == "" {
			key = joinDisplays(spec.Keys)
		}
		if key == "" {
			continue
		}
		if _, ok := byCategory[category]; !ok {
			ordered = append(ordered, category)
		}
		byCategory[category] = append(byCategory[category], Binding{Key: key, Description: help})
	}
	out := make([]Category, 0, len(ordered))
	for _, category := range ordered {
		out = append(out, Category{Name: category, Bindings: byCategory[category]})
	}
	return out
}

func specsForMode(mode types.Mode) []ActionSpec {
	out := make([]ActionSpec, 0, 16)
	for _, spec := range registry {
		if spec.Mode == mode {
			out = append(out, spec)
		}
	}
	return out
}

func joinDisplays(keys []KeySpec) string {
	displays := make([]string, 0, len(keys))
	for _, key := range keys {
		label := strings.TrimSpace(key.Display)
		if label == "" {
			continue
		}
		displays = append(displays, label)
	}
	return strings.Join(displays, "/")
}
