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
export const ANSI_ESCAPE_RE =
	/\x1b\[[0-9;]*[a-zA-Z]|\x1b\][^\x07]*\x07|\x1b[()][AB012]|\x0f|\x0e/g

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
