import { describe, expect, it } from "bun:test"
import { shouldShowActionForTmuxMode } from "./ActionPalette.js"

describe("shouldShowActionForTmuxMode", () => {
	it("hides tmux-required actions when tmux mode is disabled", () => {
		expect(shouldShowActionForTmuxMode("S", false)).toBe(false)
		expect(shouldShowActionForTmuxMode("s", false)).toBe(false)
		expect(shouldShowActionForTmuxMode("a", false)).toBe(false)
		expect(shouldShowActionForTmuxMode("r", false)).toBe(false)
		expect(shouldShowActionForTmuxMode("H", false)).toBe(false)
	})

	it("keeps non-tmux actions visible when tmux mode is disabled", () => {
		expect(shouldShowActionForTmuxMode("h", false)).toBe(true)
		expect(shouldShowActionForTmuxMode("l", false)).toBe(true)
		expect(shouldShowActionForTmuxMode("i", false)).toBe(true)
		expect(shouldShowActionForTmuxMode("f", false)).toBe(true)
		expect(shouldShowActionForTmuxMode("T", false)).toBe(true)
	})

	it("shows all actions when tmux mode is enabled", () => {
		expect(shouldShowActionForTmuxMode("S", true)).toBe(true)
		expect(shouldShowActionForTmuxMode("a", true)).toBe(true)
		expect(shouldShowActionForTmuxMode("r", true)).toBe(true)
		expect(shouldShowActionForTmuxMode("h", true)).toBe(true)
	})
})
