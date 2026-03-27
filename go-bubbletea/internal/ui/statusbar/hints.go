package statusbar

import (
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
)

// GetHintBindings returns structured key hints for the given mode.
func GetHintBindings(mode types.Mode) []keybinds.Binding {
	switch mode {
	case types.ModeNormal:
		return []keybinds.Binding{
			{Key: "Space", Description: "task workspace"},
			{Key: "g", Description: "goto"},
			{Key: "/", Description: "search"},
			{Key: "f", Description: "filter"},
			{Key: ",", Description: "sort"},
			{Key: "v", Description: "select"},
			{Key: "Enter", Description: "drill"},
			{Key: "c", Description: "create"},
			{Key: "s", Description: "settings"},
			{Key: "r", Description: "refresh"},
			{Key: "Tab", Description: "view"},
			{Key: "?", Description: "help"},
			{Key: "q", Description: "quit"},
		}
	case types.ModeGoto:
		return []keybinds.Binding{
			{Key: "g g", Description: "top"},
			{Key: "g e", Description: "bottom"},
			{Key: "g h", Description: "first col"},
			{Key: "g l", Description: "last col"},
			{Key: "g w", Description: "labels"},
			{Key: "g p", Description: "projects"},
			{Key: "g s", Description: "spec"},
			{Key: "Esc", Description: "cancel"},
		}
	case types.ModeSelect:
		return []keybinds.Binding{
			{Key: "a/5", Description: "toggle"},
			{Key: "A", Description: "column"},
			{Key: "%", Description: "all"},
			{Key: "*", Description: "invert"},
			{Key: "x", Description: "clear"},
			{Key: "Space/Enter", Description: "bulk"},
			{Key: "v/Esc", Description: "exit"},
		}
	case types.ModeSearch:
		return []keybinds.Binding{
			{Key: "Type", Description: "search"},
			{Key: "Enter", Description: "confirm"},
			{Key: "Esc", Description: "cancel"},
		}
	case types.ModeAction:
		return []keybinds.Binding{
			{Key: "h/l", Description: "move"},
			{Key: "s/S/!", Description: "start"},
			{Key: "a", Description: "attach"},
			{Key: "p", Description: "pause"},
			{Key: "R", Description: "resume"},
			{Key: "r", Description: "dev"},
			{Key: "x", Description: "stop"},
			{Key: "u", Description: "update"},
			{Key: "m/b", Description: "merge"},
			{Key: "P/O", Description: "PR"},
			{Key: "M", Description: "abort"},
			{Key: "H", Description: "helix"},
			{Key: "i", Description: "attachments"},
			{Key: "f", Description: "diff"},
			{Key: "w/W", Description: "cleanup"},
			{Key: "e", Description: "edit"},
			{Key: "c", Description: "child"},
			{Key: "T/d", Description: "tombstone/delete"},
			{Key: "Esc/q", Description: "cancel"},
		}
	default:
		return nil
	}
}

// GetHints returns a plain-text keybinding string for the given mode.
func GetHints(mode types.Mode) string {
	return keybinds.RenderPlain(GetHintBindings(mode), "  ")
}
