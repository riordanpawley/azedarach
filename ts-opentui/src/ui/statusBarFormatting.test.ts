import { describe, expect, it } from "vitest"
import { formatWaitingSummary } from "./statusBarFormatting.js"

describe("formatWaitingSummary", () => {
	it("returns null when nothing is waiting", () => {
		expect(formatWaitingSummary([], 120)).toBeNull()
	})

	it("shows a count on narrow terminals", () => {
		expect(formatWaitingSummary(["ir"], 80)).toBe("Wait: 1")
		expect(formatWaitingSummary(["ir", "fp"], 80)).toBe("Wait: 2")
	})

	it("shows the first issue on medium terminals", () => {
		expect(formatWaitingSummary(["ir"], 100)).toBe("Wait: ir")
		expect(formatWaitingSummary(["ir", "fp", "qa"], 100)).toBe("Wait: ir +2")
	})

	it("shows up to two issue IDs on wide terminals", () => {
		expect(formatWaitingSummary(["ir", "fp"], 140)).toBe("Wait: ir, fp")
		expect(formatWaitingSummary(["ir", "fp", "qa"], 140)).toBe("Wait: ir, fp +1")
	})
})
