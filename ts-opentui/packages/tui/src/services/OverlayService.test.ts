import { describe, expect, it } from "bun:test"
import { type Overlay, shouldResetScrollCommandOnPush } from "./OverlayService.js"

describe("OverlayService", () => {
	it("resets scroll command for detail and diagnostics overlays only", () => {
		expect(
			shouldResetScrollCommandOnPush({
				_tag: "detail",
				taskId: "task-1",
			}),
		).toBe(true)

		expect(
			shouldResetScrollCommandOnPush({
				_tag: "diagnostics",
			}),
		).toBe(true)

		const overlay: Overlay = {
			_tag: "help",
		}
		expect(shouldResetScrollCommandOnPush(overlay)).toBe(false)
	})
})
