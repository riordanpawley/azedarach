const PANEL_MARGIN_ROWS = 2
const PANEL_MARGIN_COLUMNS = 2
const PANEL_CHROME_ROWS = 4 // top/bottom border + top/bottom padding
const HEADER_ROWS = 3
const FOOTER_ROWS = 1

export interface DiagnosticsOverlayLayout {
	readonly panelWidth: number
	readonly panelHeight: number
	readonly dividerLength: number
	readonly scrollViewportHeight: number
}

export const computeDiagnosticsOverlayLayout = (
	terminalRows: number,
	terminalColumns: number,
): DiagnosticsOverlayLayout => {
	const panelWidth = Math.max(1, terminalColumns - PANEL_MARGIN_COLUMNS)
	const panelHeight = Math.max(1, terminalRows - PANEL_MARGIN_ROWS)
	const dividerLength = Math.max(1, panelWidth - 4)
	const scrollViewportHeight = Math.max(
		1,
		panelHeight - PANEL_CHROME_ROWS - HEADER_ROWS - FOOTER_ROWS,
	)

	return {
		panelWidth,
		panelHeight,
		dividerLength,
		scrollViewportHeight,
	}
}
