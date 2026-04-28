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
	return ClampResponsiveDialogSize(minWidth, minHeight, v.width, v.height)
}

// ClampResponsiveDialogSize applies the standard responsive overlay sizing policy.
func ClampResponsiveDialogSize(minWidth, minHeight, viewportWidth, viewportHeight int) (int, int) {
	if viewportWidth <= 0 || viewportHeight <= 0 {
		return clampDialogSize(minWidth, minHeight, viewportWidth, viewportHeight)
	}

	targetWidth := (viewportWidth * 8) / 10
	targetHeight := (viewportHeight * 8) / 10
	if targetWidth < minWidth || targetHeight < minHeight {
		return clampDialogSize(viewportWidth, viewportHeight, viewportWidth, viewportHeight)
	}
	return clampDialogSize(targetWidth, targetHeight, viewportWidth, viewportHeight)
}
