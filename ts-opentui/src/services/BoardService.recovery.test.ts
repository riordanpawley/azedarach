import { describe, expect, it } from "bun:test"
import {
	InvalidStateError,
	SessionError,
	SessionLimitError,
	SessionNotFoundError,
} from "../core/SessionManager.js"
import { TmuxError } from "../core/TmuxService.js"
import { classifySessionRecoveryError } from "./BoardService.js"

describe("BoardService session recovery classification", () => {
	it("marks tmux and session-limit errors as transient", () => {
		const tmuxError = new TmuxError({ message: "tmux server temporarily unavailable" })
		const limitError = new SessionLimitError({
			message: "Cannot recover session: Maximum session limit reached.",
			limit: 10,
			current: 10,
		})

		expect(classifySessionRecoveryError(tmuxError)).toBe("transient")
		expect(classifySessionRecoveryError(limitError)).toBe("transient")
	})

	it("marks known transient SessionError messages as transient", () => {
		const sessionError = new SessionError({
			message: "tmux resource temporarily unavailable",
			issueId: "AZE-101",
		})

		expect(classifySessionRecoveryError(sessionError)).toBe("transient")
	})

	it("marks not-found, invalid-state, and terminal SessionError failures as terminal", () => {
		const notFoundError = new SessionNotFoundError({ issueId: "AZE-102" })
		const invalidStateError = new InvalidStateError({
			issueId: "AZE-102",
			currentState: "busy",
			expectedState: "crashed",
			operation: "recoverSession",
		})
		const terminalSessionError = new SessionError({
			message: "Worktree no longer exists at /tmp/worktree. Cannot recover session.",
			issueId: "AZE-102",
		})

		expect(classifySessionRecoveryError(notFoundError)).toBe("terminal")
		expect(classifySessionRecoveryError(invalidStateError)).toBe("terminal")
		expect(classifySessionRecoveryError(terminalSessionError)).toBe("terminal")
	})
})
