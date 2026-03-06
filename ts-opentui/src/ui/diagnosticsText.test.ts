import { describe, expect, it } from "bun:test"
import { sanitizeDiagnosticInlineText, sanitizeDiagnosticTextLines } from "./diagnosticsText.js"

describe("sanitizeDiagnosticInlineText", () => {
	it("removes ANSI and control sequences and normalizes whitespace", () => {
		const input = "\u001b[31mfailure\u001b[0m \rAZE-110\tdelete:\u0007 workflowStates:\nArgumentValidation"
		expect(sanitizeDiagnosticInlineText(input)).toBe(
			"failure AZE-110 delete: workflowStates: ArgumentValidation",
		)
	})

	it("removes OSC sequences", () => {
		const input = "\u001b]0;title\u0007healthy=yes"
		expect(sanitizeDiagnosticInlineText(input)).toBe("healthy=yes")
	})
})

describe("sanitizeDiagnosticTextLines", () => {
	it("normalizes line endings and strips control sequences per line", () => {
		const input = "\u001b[32mline one\u001b[0m\r\nline\ttwo\rline\u0007 three\n"
		expect(sanitizeDiagnosticTextLines(input)).toEqual(["line one", "line two", "line three"])
	})
})
