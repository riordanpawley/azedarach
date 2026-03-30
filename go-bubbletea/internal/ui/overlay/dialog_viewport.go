package overlay

import tea "github.com/charmbracelet/bubbletea"

type dialogViewportState struct {
	width  int
	height int
}

func (v *dialogViewportState) ApplyWindowSize(msg tea.WindowSizeMsg) {
	if msg.Width > 0 {
		v.width = msg.Width
	}
	if msg.Height > 0 {
		v.height = msg.Height
	}
}

func (v dialogViewportState) Clamp(desiredWidth, desiredHeight int) (int, int) {
	return clampDialogSize(desiredWidth, desiredHeight, v.width, v.height)
}
