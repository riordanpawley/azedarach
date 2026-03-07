import { describe, expect, it } from "bun:test"
import {
	InvalidStateError,
	SessionError,
	SessionLimitError,
	SessionNotFoundError,
} from "../core/SessionManager.js"
import { TmuxError } from "../core/TmuxService.js"
import {
    classifySessionRecoveryError,
    resolveBoardRefreshExecutionMode,
    resolveLinearSdkEventsTickerBehavior,
} from "./BoardService.js"

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

describe("resolveLinearSdkEventsTickerBehavior", () => {
    it("enables slow defensive reconciliation only when sdk mode is healthy", () => {
        expect(resolveLinearSdkEventsTickerBehavior("sdk", true)).toEqual({
            localRefreshOnly: true,
            defensiveReconciliationInterval: "15 minutes",
		})
		expect(resolveLinearSdkEventsTickerBehavior("sdk", false)).toEqual({
			localRefreshOnly: false,
			defensiveReconciliationInterval: undefined,
		})
        expect(resolveLinearSdkEventsTickerBehavior("failed", false)).toEqual({
            localRefreshOnly: false,
            defensiveReconciliationInterval: undefined,
        })
    })
})

describe("resolveBoardRefreshExecutionMode", () => {
    it("forces PTY refreshes to session-only local updates", () => {
        expect(
            resolveBoardRefreshExecutionMode({
                localRefreshOnly: false,
                options: { reason: "pty" },
            }),
        ).toBe("local-session-only")
        expect(
            resolveBoardRefreshExecutionMode({
                localRefreshOnly: true,
                options: { reason: "pty" },
            }),
        ).toBe("local-session-only")
    })

    it("forces remote refresh when requested", () => {
        expect(
            resolveBoardRefreshExecutionMode({
                localRefreshOnly: true,
                options: { reason: "pty", forceRemote: true },
            }),
        ).toBe("remote")
        expect(
            resolveBoardRefreshExecutionMode({
                localRefreshOnly: true,
                options: { forceRemote: true },
            }),
        ).toBe("remote")
    })

    it("uses local session+git refresh only for webhook-local mode default reasons", () => {
        expect(
            resolveBoardRefreshExecutionMode({
                localRefreshOnly: true,
                options: { reason: "default" },
            }),
        ).toBe("local-session-and-git")
        expect(
            resolveBoardRefreshExecutionMode({
                localRefreshOnly: true,
                options: undefined,
            }),
        ).toBe("local-session-and-git")
    })
})
