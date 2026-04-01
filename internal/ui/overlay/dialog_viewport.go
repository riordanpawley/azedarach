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

func (v dialogViewportState) ClampResponsive(minWidth, minHeight int) (int, int) {
	if v.width <= 0 || v.height <= 0 {
		return v.Clamp(minWidth, minHeight)
	}

	targetWidth := (v.width * 8) / 10
	targetHeight := (v.height * 8) / 10
	if targetWidth < minWidth || targetHeight < minHeight {
		return v.Clamp(v.width, v.height)
	}
	return v.Clamp(targetWidth, targetHeight)
}
