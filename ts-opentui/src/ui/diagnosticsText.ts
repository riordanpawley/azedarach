const ANSI_ESCAPE_SEQUENCE = /\u001b(?:\[[0-?]*[ -/]*[@-~]|[@-Z\\-_])/g
const OSC_ESCAPE_SEQUENCE = /\u001b\][^\u0007]*(?:\u0007|\u001b\\)/g
const CONTROL_CHARACTERS = /[\u0000-\u0008\u000b-\u001f\u007f]/g

const stripTerminalSequences = (value: string): string =>
	value.replace(OSC_ESCAPE_SEQUENCE, "").replace(ANSI_ESCAPE_SEQUENCE, "")

export const sanitizeDiagnosticInlineText = (value: string): string =>
	stripTerminalSequences(value)
		.replace(CONTROL_CHARACTERS, "")
		.replace(/\s+/g, " ")
		.trim()

export const sanitizeDiagnosticTextLines = (value: string): readonly string[] =>
	stripTerminalSequences(value)
		.replace(/\r\n/g, "\n")
		.replace(/\r/g, "\n")
		.split("\n")
		.map((line) => line.replace(CONTROL_CHARACTERS, "").replace(/\s+/g, " ").trim())
		.filter((line) => line.length > 0)
