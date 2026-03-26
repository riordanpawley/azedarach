package statusbar

import (
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
)

// GetHints returns the keybinding hints for the given mode
func GetHints(mode types.Mode) string {
	switch mode {
	case types.ModeNormal:
		return keybinds.RenderPlain([]keybinds.Binding{
			{Key: "h/l", Description: "columns"},
			{Key: "j/k", Description: "tasks"},
			{Key: "Enter", Description: "drill"},
			{Key: "Space", Description: "details+actions"},
			{Key: "?", Description: "help"},
			{Key: "q", Description: "quit"},
		}, "  ")
	case types.ModeGoto:
		return keybinds.RenderPlain([]keybinds.Binding{
			{Key: "g", Description: "top"},
			{Key: "e", Description: "end"},
			{Key: "h", Description: "first col"},
			{Key: "l", Description: "last col"},
			{Key: "Esc", Description: "cancel"},
		}, "  ")
	case types.ModeSelect:
		return keybinds.RenderPlain([]keybinds.Binding{
			{Key: "Space", Description: "toggle"},
			{Key: "a", Description: "all"},
			{Key: "n", Description: "none"},
			{Key: "Esc", Description: "cancel"},
		}, "  ")
	case types.ModeSearch:
		return keybinds.RenderPlain([]keybinds.Binding{
			{Key: "Type to search"},
			{Key: "Enter", Description: "confirm"},
			{Key: "Esc", Description: "cancel"},
		}, "  ")
	case types.ModeAction:
		// Action mode hints will come from the action menu
		return ""
	default:
		return ""
	}
}
