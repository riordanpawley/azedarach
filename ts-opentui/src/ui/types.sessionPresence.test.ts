import { describe, expect, it } from "bun:test"
import { hasTaskSessionPresence, hasTaskWorktreeContext } from "./types.js"

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

describe("hasTaskWorktreeContext", () => {
	it("returns true when worktree is present", () => {
		expect(hasTaskWorktreeContext({ sessionState: "idle", hasWorktree: true })).toBe(true)
	})

	it("returns true when tmux session is present even if worktree flag is stale", () => {
		expect(
			hasTaskWorktreeContext({
				sessionState: "idle",
				hasWorktree: undefined,
				hasTmuxSession: true,
			}),
		).toBe(true)
	})

	it("returns false when no worktree and no session presence", () => {
		expect(
			hasTaskWorktreeContext({
				sessionState: "idle",
				hasWorktree: undefined,
				hasTmuxSession: undefined,
			}),
		).toBe(false)
	})
})
