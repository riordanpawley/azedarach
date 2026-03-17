import { describe, expect, it } from "bun:test"
import { hasTaskSessionPresence } from "./types.js"

describe("hasTaskSessionPresence", () => {
	it("returns true for non-idle session states", () => {
		expect(hasTaskSessionPresence({ sessionState: "busy" })).toBe(true)
		expect(hasTaskSessionPresence({ sessionState: "paused" })).toBe(true)
	})

	it("returns true for idle tasks with discovered tmux sessions", () => {
		expect(hasTaskSessionPresence({ sessionState: "idle", hasTmuxSession: true })).toBe(true)
	})

	it("returns false for idle tasks without tmux session presence", () => {
		expect(hasTaskSessionPresence({ sessionState: "idle", hasTmuxSession: undefined })).toBe(false)
	})
})
