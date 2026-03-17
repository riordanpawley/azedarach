import { describe, expect, it } from "bun:test"
import {
	AZEDARACH_TUI_RUNTIME_MODE_ENV,
	resolveAppStartupLayerModeFromEnv,
	resolveTuiRuntimeModeFromEnv,
} from "./runtime.js"

describe("resolveTuiRuntimeModeFromEnv", () => {
	it("defaults to daemon-rpc mode", () => {
		expect(resolveTuiRuntimeModeFromEnv({})).toBe("daemon-rpc")
	})

	it("uses fallback mode when explicitly configured", () => {
		expect(
			resolveTuiRuntimeModeFromEnv({
				[AZEDARACH_TUI_RUNTIME_MODE_ENV]: "session-manager-fallback",
			}),
		).toBe("session-manager-fallback")
	})

	it("ignores unexpected values and keeps daemon-rpc mode", () => {
		expect(
			resolveTuiRuntimeModeFromEnv({
				[AZEDARACH_TUI_RUNTIME_MODE_ENV]: "invalid",
			}),
		).toBe("daemon-rpc")
	})
})

describe("resolveAppStartupLayerModeFromEnv", () => {
	it("selects daemon-core startup mode for daemon-rpc runtime", () => {
		expect(resolveAppStartupLayerModeFromEnv({})).toBe("daemon-core")
	})

	it("selects full-deferred startup mode for session-manager fallback", () => {
		expect(
			resolveAppStartupLayerModeFromEnv({
				[AZEDARACH_TUI_RUNTIME_MODE_ENV]: "session-manager-fallback",
			}),
		).toBe("full-deferred")
	})
})
