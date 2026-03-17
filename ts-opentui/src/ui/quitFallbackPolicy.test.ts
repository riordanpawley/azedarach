import { describe, expect, it } from "bun:test"
import { shouldRequestShutdownFromDirectQuitFallback } from "./quitFallbackPolicy.js"

describe("shouldRequestShutdownFromDirectQuitFallback", () => {
	it("allows direct q quit in normal mode with no overlay", () => {
		expect(
			shouldRequestShutdownFromDirectQuitFallback({
				key: "q",
				ctrl: false,
				meta: false,
				shift: false,
				modeTag: "normal",
				hasOverlay: false,
				inDrillDown: false,
			}),
		).toBe(true)
	})

	it("blocks direct quit while any overlay is open", () => {
		expect(
			shouldRequestShutdownFromDirectQuitFallback({
				key: "q",
				ctrl: false,
				meta: false,
				shift: false,
				modeTag: "normal",
				hasOverlay: true,
				inDrillDown: false,
			}),
		).toBe(false)
	})

	it("blocks direct quit during drill-down", () => {
		expect(
			shouldRequestShutdownFromDirectQuitFallback({
				key: "q",
				ctrl: false,
				meta: false,
				shift: false,
				modeTag: "normal",
				hasOverlay: false,
				inDrillDown: true,
			}),
		).toBe(false)
	})

	it("blocks direct quit outside normal mode", () => {
		expect(
			shouldRequestShutdownFromDirectQuitFallback({
				key: "q",
				ctrl: false,
				meta: false,
				shift: false,
				modeTag: "search",
				hasOverlay: false,
				inDrillDown: false,
			}),
		).toBe(false)
	})

	it("blocks direct quit when modified or non-q key", () => {
		expect(
			shouldRequestShutdownFromDirectQuitFallback({
				key: "q",
				ctrl: true,
				meta: false,
				shift: false,
				modeTag: "normal",
				hasOverlay: false,
				inDrillDown: false,
			}),
		).toBe(false)
		expect(
			shouldRequestShutdownFromDirectQuitFallback({
				key: "escape",
				ctrl: false,
				meta: false,
				shift: false,
				modeTag: "normal",
				hasOverlay: false,
				inDrillDown: false,
			}),
		).toBe(false)
	})
})
