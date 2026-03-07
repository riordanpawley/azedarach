const PANEL_MARGIN_ROWS = 2
const PANEL_MARGIN_COLUMNS = 2
const PANEL_CHROME_HEIGHT = 8 // borders (2) + padding (2) + header (3) + footer (1)
const SCROLLBOX_OUTER_MARGIN = 4

export interface DiagnosticsOverlayLayout {
	readonly panelWidth: number
	readonly maxPanelHeight: number
	readonly dividerLength: number
	readonly maxScrollHeight: number
}

export const computeDiagnosticsOverlayLayout = (
	terminalRows: number,
	terminalColumns: number,
): DiagnosticsOverlayLayout => {
	const panelWidth = Math.max(1, terminalColumns - PANEL_MARGIN_COLUMNS)
	const maxPanelHeight = Math.max(1, terminalRows - PANEL_MARGIN_ROWS)
	const dividerLength = Math.max(1, panelWidth - 4)
	const maxScrollHeight = Math.max(10, terminalRows - PANEL_CHROME_HEIGHT - SCROLLBOX_OUTER_MARGIN)

	return {
		panelWidth,
		maxPanelHeight,
		dividerLength,
		maxScrollHeight,
	}
}
