package statusbar

import "github.com/riordanpawley/azedarach/internal/types"

// GetHints returns the keybinding hints for the given mode.
func GetHints(mode types.Mode) string {
	switch mode {
	case types.ModeNormal:
		return "Space: task workspace  g: goto  /: search  f: filter  ,: sort  v: select  Enter: drill  c: create  s: settings  r: refresh  Tab: view  ?: help  q: quit"
	case types.ModeGoto:
		return "g g: top  g e: bottom  g h: first col  g l: last col  g w: labels  g p: projects  g s: spec  Esc: cancel"
	case types.ModeSelect:
		return "a/5: toggle  A: column  %: all  *: invert  x: clear  Space/Enter: bulk  v/Esc: exit"
	case types.ModeSearch:
		return "Type: search  Enter: confirm  Esc: cancel"
	case types.ModeAction:
		return "h/l: move  s/S/!: start  a: attach  p: pause  R: resume  r: dev  x: stop  u: update  m/b: merge  P/O: PR  M: abort  H: helix  i: attachments  f: diff  w/W: cleanup  e: edit  c: child  T/d: tombstone/delete  Esc/q: cancel"
	default:
		return ""
	}
}
