import { describe, expect, it } from "bun:test"
import {
	runBoundedTeardownPhase,
	TUI_QUIT_PRESERVES_DAEMON,
	waitForShutdownCompletion,
} from "./launch.js"

describe("launch teardown helpers", () => {
	it("marks quick phases as info-complete", () => {
		const events: Array<{ level: string; phase: string; message: string; elapsedMs?: number }> = []
		runBoundedTeardownPhase({
			phase: "quick-phase",
			effect: () => {},
			warnAfterMs: 100,
			diagnostics: (event) => {
				events.push(event)
			},
		})
		expect(events.length).toBe(1)
		expect(events[0]?.level).toBe("info")
		expect(events[0]?.phase).toBe("quick-phase")
		expect(events[0]?.message).toContain("completed")
	})

	it("warns when a phase exceeds the warning budget", () => {
		const events: Array<{ level: string; phase: string; message: string; elapsedMs?: number }> = []
		runBoundedTeardownPhase({
			phase: "slow-phase",
			effect: () => {
				const startedAt = Date.now()
				while (Date.now() - startedAt < 6) {
					// busy wait in test scope to force elapsed time over warn budget
				}
			},
			warnAfterMs: 1,
			diagnostics: (event) => {
				events.push(event)
			},
		})
		expect(events.length).toBe(1)
		expect(events[0]?.level).toBe("warn")
		expect(events[0]?.phase).toBe("slow-phase")
		expect(events[0]?.message).toContain("exceeded warning budget")
	})

	it("warns when a phase throws", () => {
		const events: Array<{ level: string; phase: string; message: string; elapsedMs?: number }> = []
		runBoundedTeardownPhase({
			phase: "throwing-phase",
			effect: () => {
				throw new Error("boom")
			},
			diagnostics: (event) => {
				events.push(event)
			},
		})
		expect(events.length).toBe(1)
		expect(events[0]?.level).toBe("warn")
		expect(events[0]?.phase).toBe("throwing-phase")
		expect(events[0]?.message).toContain("boom")
	})

	it("returns completed when completion resolves within timeout", async () => {
		const completion = Promise.resolve()
		const result = await waitForShutdownCompletion({
			completion,
			timeoutMs: 25,
		})
		expect(result).toBe("completed")
	})

	it("returns timed_out and emits warning when completion does not resolve in time", async () => {
		const events: Array<{ level: string; phase: string; message: string; elapsedMs?: number }> = []
		const completion = new Promise<void>(() => {
			// intentionally unresolved in test scope
		})
		const result = await waitForShutdownCompletion({
			completion,
			timeoutMs: 5,
			diagnostics: (event) => {
				events.push(event)
			},
		})
		expect(result).toBe("timed_out")
		expect(
			events.some((event) => event.level === "warn" && event.phase === "shutdown-wait"),
		).toBeTrue()
	})
})

describe("daemon persistence invariant", () => {
	it("keeps daemon running on TUI quit", () => {
		expect(TUI_QUIT_PRESERVES_DAEMON).toBeTrue()
	})
})
