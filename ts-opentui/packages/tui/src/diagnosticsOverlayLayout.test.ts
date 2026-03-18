import { describe, expect, it } from "bun:test"
import { shouldResetScrollCommandOnPush } from "../../../src/services/OverlayService.js"
import { computeDiagnosticsOverlayLayout } from "./diagnosticsOverlayLayout.js"
import { shouldApplyDiagnosticsScrollCommand } from "./diagnosticsOverlayScroll.js"

describe("computeDiagnosticsOverlayLayout", () => {
	it("derives panel and viewport sizes from terminal size", () => {
		const layout = computeDiagnosticsOverlayLayout(50, 120)

		expect(layout.panelWidth).toBe(118)
		expect(layout.maxPanelHeight).toBe(48)
		expect(layout.dividerLength).toBe(114)
		expect(layout.maxScrollHeight).toBe(38)
	})

	it("clamps panel dimensions and enforces minimum scroll viewport", () => {
		const layout = computeDiagnosticsOverlayLayout(1, 1)

		expect(layout.panelWidth).toBe(1)
		expect(layout.maxPanelHeight).toBe(1)
		expect(layout.dividerLength).toBe(1)
		expect(layout.maxScrollHeight).toBe(10)
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

describe("shouldResetScrollCommandOnPush", () => {
	it("resets scroll command when opening scrollable overlays", () => {
		expect(shouldResetScrollCommandOnPush({ _tag: "detail", taskId: "AZE-1" })).toBe(true)
		expect(shouldResetScrollCommandOnPush({ _tag: "diagnostics" })).toBe(true)
	})

	it("does not reset scroll command for unrelated overlays", () => {
		expect(shouldResetScrollCommandOnPush({ _tag: "help" })).toBe(false)
		expect(shouldResetScrollCommandOnPush({ _tag: "settings" })).toBe(false)
	})
})
