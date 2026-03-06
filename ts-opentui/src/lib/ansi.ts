/**
 * ANSI escape code utilities for terminal output processing.
 *
 * Used to clean raw terminal output before pattern matching, improving
 * reliability of state and phase detection in PTYMonitor and StateDetector.
 */

/**
 * Regex matching common ANSI/VT100 escape sequences:
 * - CSI sequences: ESC [ ... letter (colors, cursor, erase, etc.)
 * - OSC sequences: ESC ] ... BEL (window title, etc.)
 * - Charset designators: ESC ( ) followed by A, B, 0, 1, 2
 * - Shift-in / Shift-out control characters
 */
const ESC = "\\x1b"
const BEL = "\\x07"
const SHIFT_IN = "\\x0f"
const SHIFT_OUT = "\\x0e"

export const ANSI_ESCAPE_RE = new RegExp(
	`${ESC}\\[[0-9;]*[a-zA-Z]|${ESC}\\][^${BEL}]*${BEL}|${ESC}[()][AB012]|${SHIFT_IN}|${SHIFT_OUT}`,
	"g",
)

/**
 * Strip ANSI/VT100 escape codes from terminal output.
 *
 * Removes color codes, cursor movement, and other control sequences so that
 * text patterns can be matched reliably against plain content.
 *
 * @param text - raw terminal output, may contain ANSI escape sequences
 * @returns plain text with all ANSI codes removed
 */
export const stripAnsi = (text: string): string => text.replace(ANSI_ESCAPE_RE, "")
