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

	it("ignores fallback env setting and keeps daemon-rpc mode", () => {
		expect(
			resolveTuiRuntimeModeFromEnv({
				[AZEDARACH_TUI_RUNTIME_MODE_ENV]: "session-manager-fallback",
			}),
		).toBe("daemon-rpc")
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

	it("ignores fallback env setting and stays on daemon-core startup mode", () => {
		expect(
			resolveAppStartupLayerModeFromEnv({
				[AZEDARACH_TUI_RUNTIME_MODE_ENV]: "session-manager-fallback",
			}),
		).toBe("daemon-core")
	})
})
