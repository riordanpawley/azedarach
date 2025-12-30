package statusbar

import "github.com/riordanpawley/azedarach/internal/types"

// GetHints returns the keybinding hints for the given mode
func GetHints(mode types.Mode) string {
	switch mode {
	case types.ModeNormal:
		return "h/l: columns  j/k: tasks  Space: action  ?: help  q: quit"
	case types.ModeGoto:
		return "g: top  e: end  h: first col  l: last col  Esc: cancel"
	case types.ModeSelect:
		return "Space: toggle  a: all  n: none  Esc: cancel"
	case types.ModeSearch:
		return "Type to search  Enter: confirm  Esc: cancel"
	case types.ModeAction:
		// Action mode hints will come from the action menu
		return ""
	default:
		return ""
	}
}
