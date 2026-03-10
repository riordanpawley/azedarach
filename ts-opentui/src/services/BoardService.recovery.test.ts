import { describe, expect, it } from "bun:test"
import {
	InvalidStateError,
	SessionError,
	SessionLimitError,
	SessionNotFoundError,
} from "../core/SessionManager.js"
import { TmuxError } from "../core/TmuxService.js"
import {
	applySessionRefreshPatch,
	classifySessionRecoveryError,
	reconcileLoadedTasksWithLocalCreateGrace,
	resolveBoardRefreshExecutionMode,
	resolveHasWorktreeFlag,
	resolveLinearSdkEventsTickerBehavior,
	resolveLinearSdkPollingFallbackHealthMessage,
	resolveLinearSdkPollingFallbackToastMessage,
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
		const sqliteLockError = new SessionError({
			message: "SQLite operation failed: database is locked",
			issueId: "AZE-101",
		})

		expect(classifySessionRecoveryError(sessionError)).toBe("transient")
		expect(classifySessionRecoveryError(sqliteLockError)).toBe("transient")
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
			defensiveReconciliationInterval: "2 minutes",
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

describe("linear SDK polling fallback messaging", () => {
	it("uses reason-aware toast text for missing public URL misconfiguration", () => {
		expect(
			resolveLinearSdkPollingFallbackToastMessage({
				mode: "misconfigured",
				reason:
					'Linear webhook SDK mode requires a public webhook URL. Set issueTracker.linear.webhooks.url, export LINEAR_WEBHOOK_PUBLIC_URL, or run "tailscale funnel --bg --yes 9000"',
			}),
		).toContain("public webhook URL")
	})

	it("preserves non-url reasons in toast text", () => {
		expect(
			resolveLinearSdkPollingFallbackToastMessage({
				mode: "misconfigured",
				reason:
					"Linear webhook SDK mode found multiple teams (AZE, OPS); set issueTracker.linear.team",
			}),
		).toBe(
			"Linear webhooks unavailable (mode=misconfigured): Linear webhook SDK mode found multiple teams (AZE, OPS); set issueTracker.linear.team. Falling back to background polling.",
		)
	})

	it("includes runtime reason in diagnostics health message", () => {
		expect(
			resolveLinearSdkPollingFallbackHealthMessage({
				mode: "failed",
				healthy: false,
				reason: "Timed out registering Linear webhook after 4000ms",
			}),
		).toBe(
			"SDK mode=failed healthy=false with no CLI fallback; reason=Timed out registering Linear webhook after 4000ms; using background polling.",
		)
	})
})

describe("applySessionRefreshPatch", () => {
	const baseTask = {
		id: "AZE-1",
		title: "Task",
		status: "open",
		priority: 2,
		issue_type: "task",
		created_at: "2026-03-07T00:00:00.000Z",
		updated_at: "2026-03-07T00:00:00.000Z",
		implementations: ["default"],
		sessionState: "busy",
		gitBehindCount: 3,
		hasUncommittedChanges: true,
		gitAdditions: 20,
		gitDeletions: 5,
	} as const

	it("preserves existing git fields when gitStatusPatch is undefined", () => {
		const updated = applySessionRefreshPatch({
			task: baseTask,
			sessionState: "waiting",
			sessionStartedAt: "2026-03-07T00:10:00.000Z",
			estimatedTokens: 1234,
			recentOutput: "working",
			agentPhase: "planning",
			gitStatusPatch: undefined,
		})

		expect(updated.sessionState).toBe("waiting")
		expect(updated.gitBehindCount).toBe(3)
		expect(updated.hasUncommittedChanges).toBe(true)
		expect(updated.gitAdditions).toBe(20)
		expect(updated.gitDeletions).toBe(5)
	})

	it("updates git fields when gitStatusPatch is provided", () => {
		const updated = applySessionRefreshPatch({
			task: baseTask,
			sessionState: "busy",
			sessionStartedAt: "2026-03-07T00:10:00.000Z",
			estimatedTokens: 1500,
			recentOutput: "running",
			agentPhase: "action",
			gitStatusPatch: {
				gitBehindCount: 0,
				hasUncommittedChanges: false,
				gitAdditions: 0,
				gitDeletions: 0,
			},
		})

		expect(updated.gitBehindCount).toBe(0)
		expect(updated.hasUncommittedChanges).toBe(false)
		expect(updated.gitAdditions).toBe(0)
		expect(updated.gitDeletions).toBe(0)
	})
})

describe("reconcileLoadedTasksWithLocalCreateGrace", () => {
	const localOnlyTask = {
		id: "AZE-42",
		title: "Created locally",
		status: "open",
		priority: 2,
		issue_type: "task",
		created_at: "2026-03-07T00:00:00.000Z",
		updated_at: "2026-03-07T00:00:05.000Z",
		implementations: ["default"],
		sessionState: "idle",
	} as const

	it("retains recently created local task when refresh payload is temporarily missing it", () => {
		const result = reconcileLoadedTasksWithLocalCreateGrace({
			loadedTasks: [],
			currentTasks: [localOnlyTask],
			localCreateGraceExpiries: new Map([[localOnlyTask.id, 10_000]]),
			nowMs: 5_000,
		})

		expect(result.mergedTasks.map((task) => task.id)).toEqual([localOnlyTask.id])
		expect(result.nextLocalCreateGraceExpiries.get(localOnlyTask.id)).toBe(10_000)
	})

	it("drops local-create grace entry once backend includes the task", () => {
		const result = reconcileLoadedTasksWithLocalCreateGrace({
			loadedTasks: [localOnlyTask],
			currentTasks: [localOnlyTask],
			localCreateGraceExpiries: new Map([[localOnlyTask.id, 10_000]]),
			nowMs: 5_000,
		})

		expect(result.mergedTasks.map((task) => task.id)).toEqual([localOnlyTask.id])
		expect(result.nextLocalCreateGraceExpiries.has(localOnlyTask.id)).toBe(false)
	})

	it("stops retaining task after grace window expires", () => {
		const result = reconcileLoadedTasksWithLocalCreateGrace({
			loadedTasks: [],
			currentTasks: [localOnlyTask],
			localCreateGraceExpiries: new Map([[localOnlyTask.id, 10_000]]),
			nowMs: 10_000,
		})

		expect(result.mergedTasks).toEqual([])
		expect(result.nextLocalCreateGraceExpiries.has(localOnlyTask.id)).toBe(false)
	})
})

describe("resolveHasWorktreeFlag", () => {
	it("prefers fresh worktree inventory over stale persisted state", () => {
		expect(
			resolveHasWorktreeFlag({
				issueId: "jt",
				persistedHasWorktree: true,
				worktreeIssueIds: new Set<string>(),
				worktreeInventoryLoaded: true,
			}),
		).toBeUndefined()
	})

	it("keeps the folder indicator when fresh inventory confirms the worktree exists", () => {
		expect(
			resolveHasWorktreeFlag({
				issueId: "jt",
				persistedHasWorktree: undefined,
				worktreeIssueIds: new Set(["jt"]),
				worktreeInventoryLoaded: true,
			}),
		).toBe(true)
	})

	it("falls back to persisted state when worktree inventory is unavailable", () => {
		expect(
			resolveHasWorktreeFlag({
				issueId: "jt",
				persistedHasWorktree: true,
				worktreeIssueIds: new Set<string>(),
				worktreeInventoryLoaded: false,
			}),
		).toBe(true)
	})
})
