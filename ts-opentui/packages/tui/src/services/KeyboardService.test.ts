import { describe, expect, it } from "bun:test"
import { KeyboardService } from "./KeyboardService.js"

describe("KeyboardService", () => {
	it("exposes a service tag", () => {
		expect(KeyboardService).toBeDefined()
	})
})
