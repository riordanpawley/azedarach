const ESC = "\\x1b"
const BEL = "\\x07"
const SHIFT_IN = "\\x0f"
const SHIFT_OUT = "\\x0e"

export const ANSI_ESCAPE_RE = new RegExp(
	`${ESC}\\[[0-9;]*[a-zA-Z]|${ESC}\\][^${BEL}]*${BEL}|${ESC}[()][AB012]|${SHIFT_IN}|${SHIFT_OUT}`,
	"g",
)

export const stripAnsi = (text: string): string => text.replace(ANSI_ESCAPE_RE, "")
