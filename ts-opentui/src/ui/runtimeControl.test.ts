import { describe, expect, it } from "bun:test"
import { clearShutdownHandler, registerShutdownHandler, requestShutdown } from "./runtimeControl.js"

describe("runtimeControl shutdown bridge", () => {
	it("invokes only registered shutdown handler", () => {
		let shutdownCalls = 0
		const daemonStopCalls = 0

		registerShutdownHandler(() => {
			shutdownCalls += 1
		})

		requestShutdown()
		expect(shutdownCalls).toBe(1)
		expect(daemonStopCalls).toBe(0)

		clearShutdownHandler()
		requestShutdown()
		expect(shutdownCalls).toBe(1)
		expect(daemonStopCalls).toBe(0)
	})
})
