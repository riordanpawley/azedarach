/**
 * Responsive layout helpers for terminal-based UI.
 */

/**
 * Breakpoint for "small screen" terminal layout.
 * Below this width, kanban switches to one-column mode and overlays compact.
 */
export const SMALL_SCREEN_COLUMNS = 110

/**
 * Returns current terminal width, with a safe fallback.
 */
export const getTerminalColumns = (): number => process.stdout.columns || 80

/**
 * True when the terminal should use small-screen responsive layouts.
 */
export const isSmallScreen = (columns: number = getTerminalColumns()): boolean =>
	columns < SMALL_SCREEN_COLUMNS
