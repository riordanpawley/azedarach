import { describe, expect, it } from "bun:test"
import {
	ACTIVE_SESSION_STATES,
	classifySessionStateForMissingCodeWindow,
	isActiveSessionState,
	resolveDiscoveredSessionState,
	resolveSessionStateFromTmuxStatus,
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

	it("treats synthetic tmux disappearances for active sessions as crashed", () => {
		expect(resolveSessionStateFromTmuxStatus("busy", "idle", true)).toBe("crashed")
		expect(resolveSessionStateFromTmuxStatus("waiting", "idle", true)).toBe("crashed")
		expect(resolveSessionStateFromTmuxStatus("paused", "idle", true)).toBe("crashed")
	})

	it("does not demote repeated synthetic disappearances once a session is crashed", () => {
		expect(resolveSessionStateFromTmuxStatus("crashed", "idle", true)).toBe("crashed")
	})

	it("keeps real idle hooks and terminal states distinct from synthetic disappearance", () => {
		expect(resolveSessionStateFromTmuxStatus("busy", "idle", false)).toBe("idle")
		expect(resolveSessionStateFromTmuxStatus("done", "idle", true)).toBe("idle")
		expect(resolveSessionStateFromTmuxStatus("error", "idle", true)).toBe("idle")
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
