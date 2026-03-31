package overlay

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
)

// twoPaneDialogChrome standardizes dialog overlays that render their own
// title/border and should not be wrapped by the app-level overlay frame.
type twoPaneDialogChrome struct{}

func (twoPaneDialogChrome) UsesAppFrame() bool {
	return false
}

func (twoPaneDialogChrome) UsesInternalTitle() bool {
	return true
}

func renderDialogActions(styles *Styles, bindings []keybinds.Binding, width ...int) string {
	maxWidth := 0
	if len(width) > 0 {
		maxWidth = width[0]
	}

	usable := make([]keybinds.Binding, 0, len(bindings))
	for _, binding := range bindings {
		if strings.TrimSpace(binding.Key) == "" {
			continue
		}
		usable = append(usable, binding)
	}
	if len(usable) == 0 {
		return ""
	}

	lines := make([]string, 0, len(usable))
	for _, binding := range usable {
		keyLabel := strings.TrimSpace(binding.Key)
		line := " " + styles.MenuKey.Render(keyLabel)
		desc := strings.TrimSpace(binding.Description)
		if desc != "" {
			if maxWidth > 0 {
				available := max(4, maxWidth-(1+ansi.StringWidth(keyLabel)+1))
				desc = ansi.Truncate(desc, available, "...")
			}
			line += " " + styles.MenuItem.Render(desc)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
