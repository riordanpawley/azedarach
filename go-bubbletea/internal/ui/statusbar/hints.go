package statusbar

import "github.com/riordanpawley/azedarach/internal/app"

// GetHints returns the keybinding hints for the given mode
func GetHints(mode app.Mode) string {
	switch mode {
	case app.ModeNormal:
		return "h/l: columns  j/k: tasks  Space: action  ?: help  q: quit"
	case app.ModeGoto:
		return "g: top  e: end  h: first col  l: last col  Esc: cancel"
	case app.ModeSelect:
		return "Space: toggle  a: all  n: none  Esc: cancel"
	case app.ModeSearch:
		return "Type to search  Enter: confirm  Esc: cancel"
	case app.ModeAction:
		// Action mode hints will come from the action menu
		return ""
	default:
		return ""
	}
}
