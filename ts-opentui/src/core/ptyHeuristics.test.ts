import { describe, expect, it } from "bun:test"
import {
	deriveShellForegroundState,
	shouldApplyHighPriorityDetectedState,
} from "./ptyHeuristics.js"

describe("ptyHeuristics", () => {
	it("treats a fresh shell bell edge as waiting", () => {
		expect(
			deriveShellForegroundState({
				currentState: "busy",
				foregroundKind: "shell",
				bellFlag: true,
				previousBellFlag: false,
				detectedState: "waiting",
			}),
		).toBe("waiting")
	})

	it("treats shell foreground without a fresh bell as done", () => {
		expect(
			deriveShellForegroundState({
				currentState: "busy",
				foregroundKind: "shell",
				bellFlag: false,
				previousBellFlag: false,
				detectedState: "waiting",
			}),
		).toBe("done")
		expect(
			deriveShellForegroundState({
				currentState: "initializing",
				foregroundKind: "shell",
				bellFlag: true,
				previousBellFlag: true,
				detectedState: "waiting",
			}),
		).toBe("done")
	})

	it("treats shell bell edge as done when no waiting prompt is detected", () => {
		expect(
			deriveShellForegroundState({
				currentState: "busy",
				foregroundKind: "shell",
				bellFlag: true,
				previousBellFlag: false,
				detectedState: "busy",
			}),
		).toBe("done")
	})

	it("ignores shell foreground for non-active session states", () => {
		expect(
			deriveShellForegroundState({
				currentState: "waiting",
				foregroundKind: "shell",
				bellFlag: true,
				previousBellFlag: false,
				detectedState: "waiting",
			}),
		).toBeNull()
		expect(
			deriveShellForegroundState({
				currentState: "busy",
				foregroundKind: "agent",
				bellFlag: false,
				previousBellFlag: false,
				detectedState: "busy",
			}),
		).toBeNull()
	})

	it("allows waiting transitions while the session is active", () => {
		expect(
			shouldApplyHighPriorityDetectedState({
				currentState: "busy",
				detectedState: "waiting",
				foregroundKind: "agent",
			}),
		).toBe(true)
	})

	it("suppresses error transitions while the agent remains foreground", () => {
		expect(
			shouldApplyHighPriorityDetectedState({
				currentState: "busy",
				detectedState: "error",
				foregroundKind: "agent",
			}),
		).toBe(false)
	})

	it("suppresses error transitions while a spawned subprocess is foreground", () => {
		expect(
			shouldApplyHighPriorityDetectedState({
				currentState: "busy",
				detectedState: "error",
				foregroundKind: "subprocess",
			}),
		).toBe(false)
	})

	it("still allows error transitions once the agent is no longer foreground", () => {
		expect(
			shouldApplyHighPriorityDetectedState({
				currentState: "waiting",
				detectedState: "error",
				foregroundKind: "unknown",
			}),
		).toBe(true)
	})
})
