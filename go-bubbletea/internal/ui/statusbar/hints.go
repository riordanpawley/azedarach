package statusbar

import (
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
)

// GetHintBindings returns structured key hints for the given mode.
func GetHintBindings(mode types.Mode) []keybinds.Binding {
	return keybinds.HintBindings(mode)
}

// GetHints returns a plain-text keybinding string for the given mode.
func GetHints(mode types.Mode) string {
	return keybinds.RenderPlain(GetHintBindings(mode), "  ")
}
