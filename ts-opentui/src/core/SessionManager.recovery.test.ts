import { describe, expect, it } from "bun:test"
import {
	ACTIVE_SESSION_STATES,
	classifySessionStateForMissingCodeWindow,
	isActiveSessionState,
	resolveDiscoveredSessionState,
} from "./SessionManager.js"

describe("SessionManager discovered session recovery", () => {
	it("marks active sessions without code window as crashed", () => {
		expect(resolveDiscoveredSessionState("initializing", false)).toBe("crashed")
		expect(resolveDiscoveredSessionState("busy", false)).toBe("crashed")
		expect(resolveDiscoveredSessionState(undefined, false)).toBe("crashed")
	})

	it("preserves non-active states when code window is missing", () => {
		expect(resolveDiscoveredSessionState("done", false)).toBe("done")
		expect(resolveDiscoveredSessionState("error", false)).toBe("error")
	})

	it("defaults to busy when code window exists and no persisted state is available", () => {
		expect(resolveDiscoveredSessionState(undefined, true)).toBe("busy")
	})

	it("does not mark sessions as crashed while startup is in progress", () => {
		expect(resolveDiscoveredSessionState("initializing", false, true)).toBe("initializing")
		expect(resolveDiscoveredSessionState(undefined, false, true)).toBe("busy")
	})

	it("uses shared missing-window classifier for startup lock cases", () => {
		expect(
			classifySessionStateForMissingCodeWindow("initializing", {
				hasCodeWindow: false,
				hasStartLock: true,
				tmuxStartupInProgress: false,
			}),
		).toBe("initializing")
		expect(
			classifySessionStateForMissingCodeWindow(undefined, {
				hasCodeWindow: false,
				hasStartLock: true,
				tmuxStartupInProgress: false,
			}),
		).toBe("busy")
	})

	it("tracks active states consistently", () => {
		expect(ACTIVE_SESSION_STATES.has("waiting")).toBe(true)
		expect(isActiveSessionState("paused")).toBe(true)
		expect(isActiveSessionState("done")).toBe(false)
	})
})
