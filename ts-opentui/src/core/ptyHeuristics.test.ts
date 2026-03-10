import { describe, expect, it } from "vitest"
import { deriveShellForegroundState } from "./ptyHeuristics.js"

describe("deriveShellForegroundState", () => {
	it("ignores non-shell foreground states", () => {
		expect(
			deriveShellForegroundState({
				currentState: "busy",
				foregroundKind: "agent",
				bellFlag: true,
				previousBellFlag: false,
			}),
		).toBeNull()
	})

	it("ignores inactive session states", () => {
		expect(
			deriveShellForegroundState({
				currentState: "waiting",
				foregroundKind: "shell",
				bellFlag: true,
				previousBellFlag: false,
			}),
		).toBeNull()
	})

	it("treats a fresh bell edge as waiting", () => {
		expect(
			deriveShellForegroundState({
				currentState: "busy",
				foregroundKind: "shell",
				bellFlag: true,
				previousBellFlag: false,
			}),
		).toBe("waiting")
	})

	it("falls back to done without a fresh bell edge", () => {
		expect(
			deriveShellForegroundState({
				currentState: "busy",
				foregroundKind: "shell",
				bellFlag: false,
				previousBellFlag: false,
			}),
		).toBe("done")
		expect(
			deriveShellForegroundState({
				currentState: "initializing",
				foregroundKind: "shell",
				bellFlag: true,
				previousBellFlag: true,
			}),
		).toBe("done")
	})
})
