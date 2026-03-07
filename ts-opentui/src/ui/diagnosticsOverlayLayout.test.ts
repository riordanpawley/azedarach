import { describe, expect, it } from "bun:test"
import { computeDiagnosticsOverlayLayout } from "./diagnosticsOverlayLayout.js"
import { shouldApplyDiagnosticsScrollCommand } from "./diagnosticsOverlayScroll.js"

describe("computeDiagnosticsOverlayLayout", () => {
	it("derives panel and viewport sizes from terminal size", () => {
		const layout = computeDiagnosticsOverlayLayout(50, 120)

		expect(layout.panelWidth).toBe(118)
		expect(layout.panelHeight).toBe(48)
		expect(layout.dividerLength).toBe(114)
		expect(layout.scrollViewportHeight).toBe(40)
	})

	it("clamps all dimensions to at least 1", () => {
		const layout = computeDiagnosticsOverlayLayout(1, 1)

		expect(layout.panelWidth).toBe(1)
		expect(layout.panelHeight).toBe(1)
		expect(layout.dividerLength).toBe(1)
		expect(layout.scrollViewportHeight).toBe(1)
	})
})

describe("shouldApplyDiagnosticsScrollCommand", () => {
	it("rejects stale commands emitted before overlay open", () => {
		const shouldApply = shouldApplyDiagnosticsScrollCommand(
			{
				target: "diagnostics",
				type: "line",
				amount: 1,
				timestamp: 100,
			},
			101,
			null,
		)

		expect(shouldApply).toBe(false)
	})

	it("rejects duplicate command timestamps", () => {
		const shouldApply = shouldApplyDiagnosticsScrollCommand(
			{
				target: "diagnostics",
				type: "line",
				amount: 1,
				timestamp: 200,
			},
			100,
			200,
		)

		expect(shouldApply).toBe(false)
	})

	it("accepts fresh diagnostics commands", () => {
		const shouldApply = shouldApplyDiagnosticsScrollCommand(
			{
				target: "diagnostics",
				type: "halfPage",
				amount: 1,
				timestamp: 300,
			},
			100,
			200,
		)

		expect(shouldApply).toBe(true)
	})

	it("rejects commands for other targets", () => {
		const shouldApply = shouldApplyDiagnosticsScrollCommand(
			{
				target: "detail",
				type: "line",
				amount: 1,
				timestamp: 300,
			},
			100,
			null,
		)

		expect(shouldApply).toBe(false)
	})
})
