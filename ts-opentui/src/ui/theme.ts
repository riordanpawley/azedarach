/**
 * Catppuccin Mocha theme colors
 *
 * A soothing pastel color palette for the UI.
 * @see https://catppuccin.com/palette/
 */

export const theme = {
	// Base colors
	base: "#1e1e2e",
	mantle: "#181825",
	crust: "#11111b",

	// Text colors
	text: "#cdd6f4",
	subtext1: "#bac2de",
	subtext0: "#a6adc8",

	// Overlay colors
	overlay2: "#9399b2",
	overlay1: "#7f849c",
	overlay0: "#6c7086",

	// Surface colors
	surface2: "#585b70",
	surface1: "#45475a",
	surface0: "#313244",

	// Accent colors
	red: "#f38ba8",
	green: "#a6e3a1",
	blue: "#89b4fa",
	yellow: "#f9e2af",
	peach: "#fab387",
	mauve: "#cba6f7",
	pink: "#f5c2e7",
	teal: "#94e2d5",
	sky: "#89dceb",
	sapphire: "#74c7ec",
	lavender: "#b4befe",
	flamingo: "#f2cdcd",
	rosewater: "#f5e0dc",
	maroon: "#eba0ac",
} as const

export type ThemeColor = keyof typeof theme

/**
 * Get priority color from theme
 */
export function getPriorityColor(priority: number): string {
	if (priority <= 1) return theme.red
	if (priority <= 2) return theme.peach
	if (priority <= 3) return theme.yellow
	if (priority <= 5) return theme.text
	return theme.subtext0
}

/**
 * Column header colors
 */
export const columnColors = {
	open: theme.blue,
	in_progress: theme.mauve,
	blocked: theme.red,
	review: theme.yellow,
	closed: theme.green,
} as const
