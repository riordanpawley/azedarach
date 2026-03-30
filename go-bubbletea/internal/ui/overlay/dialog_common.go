package overlay

import "github.com/riordanpawley/azedarach/internal/ui/keybinds"

// twoPaneDialogChrome standardizes dialog overlays that render their own
// title/border and should not be wrapped by the app-level overlay frame.
type twoPaneDialogChrome struct{}

func (twoPaneDialogChrome) UsesAppFrame() bool {
	return false
}

func (twoPaneDialogChrome) UsesInternalTitle() bool {
	return true
}

func renderDialogActions(styles *Styles, bindings []keybinds.Binding) string {
	return keybinds.RenderKeyTable(bindings, 0, keybinds.Theme{
		KeyStyle:         styles.MenuKey,
		DescriptionStyle: styles.Footer,
		FooterStyle:      styles.Footer,
	})
}
