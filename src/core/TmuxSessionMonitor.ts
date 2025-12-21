/**
 * TmuxSessionMonitor - Effect service for monitoring Claude Code session state
 *
 * Polls tmux session options to detect session state changes set by
 * `az notify` commands. This enables authoritative state detection
 * from Claude Code's native hook system.
 *
 * State detection flow:
 * 1. Claude Code hooks call `az notify <event> <beadId>`
 * 2. `az notify` sets tmux session option `@az_status` on the Claude session
 * 3. TmuxSessionMonitor polls tmux sessions and reads their `@az_status`
 * 4. State changes trigger handler callbacks
 *
 * Status values:
 * - "busy" → SessionState "busy" (Claude is working)
 * - "waiting" → SessionState "waiting" (Claude awaits input)
 * - "idle" → SessionState "idle" (Session ended)
 */

import { Command } from "@effect/platform"
import { Data, Effect, type Fiber, Ref, Schedule, type Scope } from "effect"
import { DiagnosticsService } from "../services/DiagnosticsService.js"
import { CLAUDE_SESSION_PREFIX, parseSessionName } from "./paths.js"

// ============================================================================
// Type Definitions
// ============================================================================

/**
 * Tmux status values (what `az notify` sets)
 */
export type TmuxStatus = "busy" | "waiting" | "idle"

/**
 * Session state update from tmux polling
 */
export interface SessionStateUpdate {
	readonly beadId: string
	readonly status: TmuxStatus
	readonly sessionName: string
	/** Unix timestamp when the tmux session was created */
	readonly createdAt: number
	/** Path to the worktree directory (from @az_worktree option) */
	readonly worktreePath: string | null
	/** Path to the main project directory (from @az_project option) */
	readonly projectPath: string | null
}

/**
 * Callback for processing state updates
 */
export type StateUpdateHandler = (update: SessionStateUpdate) => Effect.Effect<void, never>

// ============================================================================
// Error Types
// ============================================================================

/**
 * Error when session monitor fails
 */
export class TmuxSessionMonitorError extends Data.TaggedError("TmuxSessionMonitorError")<{
	readonly message: string
	readonly cause?: unknown
}> {}

// ============================================================================
// Constants
// ============================================================================

/**
 * Polling interval for watching tmux sessions
 */
const POLL_INTERVAL_MS = 500

// ============================================================================
// Service Definition
// ============================================================================

/**
 * TmuxSessionMonitor service interface
 *
 * Provides tmux session polling capabilities for detecting session state
 * from Claude Code sessions running in worktrees.
 */
export interface TmuxSessionMonitorService {
	/**
	 * Start watching for session state changes
	 *
	 * Returns a Fiber that can be interrupted to stop watching.
	 * State changes are pushed to the provided handler.
	 *
	 * IMPORTANT: The returned fiber is scoped to the caller's scope.
	 * Use Effect.forkScoped internally so the fiber survives after start() returns.
	 */
	readonly start: (
		handler: StateUpdateHandler,
	) => Effect.Effect<Fiber.RuntimeFiber<number, never>, never, Scope.Scope>

	/**
	 * Get current status for a specific session
	 */
	readonly getSessionStatus: (beadId: string) => Effect.Effect<TmuxStatus | null, never>

	/**
	 * Get session creation time (Unix timestamp)
	 *
	 * Uses tmux's built-in #{session_created} variable.
	 */
	readonly getSessionCreatedAt: (beadId: string) => Effect.Effect<number | null, never>

	/**
	 * List all active Claude sessions with their status
	 */
	readonly listSessions: () => Effect.Effect<readonly SessionStateUpdate[], never>
}

// ============================================================================
// Service Implementation
// ============================================================================

/**
 * TmuxSessionMonitor service
 *
 * Polls tmux sessions to detect state changes set by `az notify` hooks.
 * Uses tmux session option `@az_status` for IPC.
 *
 * @example
 * ```ts
 * const program = Effect.gen(function* () {
 *   const monitor = yield* TmuxSessionMonitor
 *   const fiber = yield* monitor.start((update) =>
 *     Effect.gen(function* () {
 *       const newState = mapStatusToState(update.status)
 *       yield* sessionManager.updateState(update.beadId, newState)
 *     })
 *   )
 *   // Later: yield* Fiber.interrupt(fiber)
 * }).pipe(Effect.provide(TmuxSessionMonitor.Default))
 * ```
 */
export class TmuxSessionMonitor extends Effect.Service<TmuxSessionMonitor>()("TmuxSessionMonitor", {
	dependencies: [DiagnosticsService.Default],
	scoped: Effect.gen(function* () {
		const diagnostics = yield* DiagnosticsService

		// Track previous state to detect changes
		const previousStateRef = yield* Ref.make<Map<string, TmuxStatus>>(new Map())

		/**
		 * List all tmux sessions starting with "claude-"
		 * Returns session name and creation timestamp in one call.
		 */
		const listClaudeSessions = () =>
			Effect.gen(function* () {
				// Get both session name and creation time in one tmux call
				// Format: "session_name|unix_timestamp"
				const command = Command.make(
					"tmux",
					"list-sessions",
					"-F",
					"#{session_name}|#{session_created}",
				)

				const output = yield* Command.string(command).pipe(
					Effect.catchAll(() => Effect.succeed("")),
				)

				return output
					.split("\n")
					.map((line) => line.trim())
					.filter((line) => line.startsWith(CLAUDE_SESSION_PREFIX))
					.map((line) => {
						const [name, createdStr] = line.split("|")
						return {
							name,
							createdAt: parseInt(createdStr, 10) || 0,
						}
					})
			})

		/**
		 * Get a tmux session option by name
		 */
		const getTmuxOption = (sessionName: string, optionName: string) =>
			Effect.gen(function* () {
				const command = Command.make("tmux", "show-option", "-t", sessionName, "-v", optionName)

				const output = yield* Command.string(command).pipe(
					Effect.catchAll(() => Effect.succeed("")),
				)

				const value = output.trim()
				return value || null
			})

		/**
		 * Get the @az_status option for a tmux session
		 */
		const getSessionOption = (sessionName: string) =>
			Effect.gen(function* () {
				const status = yield* getTmuxOption(sessionName, "@az_status")
				if (status === "busy" || status === "waiting" || status === "idle") {
					return status
				}
				return null
			})

		/**
		 * Extract bead ID from session name
		 *
		 * Handles both formats:
		 * - New: claude-{projectName}-{beadId} → extracts beadId
		 * - Legacy: claude-{beadId} → extracts beadId (when beadId has 2 parts like "az-123")
		 */
		const extractBeadId = (sessionName: string): string | null => {
			// Try new format first using parseSessionName
			const parsed = parseSessionName(sessionName)
			if (parsed && parsed.type === "claude") {
				return parsed.beadId
			}

			// Fallback: legacy format claude-{beadId} where beadId is like "az-123"
			if (sessionName.startsWith(CLAUDE_SESSION_PREFIX)) {
				const rest = sessionName.slice(CLAUDE_SESSION_PREFIX.length)
				// Check if it looks like a bead ID (prefix-suffix pattern)
				const beadIdPattern = /^[a-z]+-[a-z0-9]+$/i
				if (beadIdPattern.test(rest)) {
					return rest
				}
			}

			return null
		}

		/**
		 * List all sessions with their current status and creation time
		 */
		const listSessions = () =>
			Effect.gen(function* () {
				const sessions = yield* listClaudeSessions()
				const results: SessionStateUpdate[] = []

				for (const session of sessions) {
					const beadId = extractBeadId(session.name)
					if (!beadId) continue

					const status = yield* getSessionOption(session.name)
					if (status) {
						// Fetch worktree and project paths from tmux session options
						const worktreePath = yield* getTmuxOption(session.name, "@az_worktree")
						const projectPath = yield* getTmuxOption(session.name, "@az_project")

						results.push({
							beadId,
							status,
							sessionName: session.name,
							createdAt: session.createdAt,
							worktreePath,
							projectPath,
						})
					}
				}

				return results
			})

		/**
		 * Find the tmux session name for a given beadId
		 *
		 * Searches through running sessions to find one that matches the beadId.
		 * Handles both new format (claude-{project}-{beadId}) and legacy (claude-{beadId}).
		 */
		const findSessionByBeadId = (beadId: string) =>
			Effect.gen(function* () {
				const sessions = yield* listClaudeSessions()
				for (const session of sessions) {
					const sessionBeadId = extractBeadId(session.name)
					if (sessionBeadId === beadId) {
						return session.name
					}
				}
				return null
			})

		/**
		 * Get status for a specific session
		 *
		 * Searches for the session by beadId since we don't know the project name.
		 */
		const getSessionStatus = (beadId: string) =>
			Effect.gen(function* () {
				const sessionName = yield* findSessionByBeadId(beadId)
				if (!sessionName) return null
				return yield* getSessionOption(sessionName)
			})

		/**
		 * Get session creation time (Unix timestamp)
		 * Uses tmux's built-in #{session_created} variable.
		 *
		 * Searches for the session by beadId since we don't know the project name.
		 */
		const getSessionCreatedAt = (beadId: string) =>
			Effect.gen(function* () {
				const sessionName = yield* findSessionByBeadId(beadId)
				if (!sessionName) return null

				const command = Command.make(
					"tmux",
					"display",
					"-t",
					sessionName,
					"-p",
					"#{session_created}",
				)

				const output = yield* Command.string(command).pipe(
					Effect.catchAll(() => Effect.succeed("")),
				)

				const timestamp = parseInt(output.trim(), 10)
				return Number.isNaN(timestamp) ? null : timestamp
			})

		/**
		 * Start polling for state changes
		 */
		const start = (handler: StateUpdateHandler) =>
			Effect.gen(function* () {
				// Initial poll to populate state
				const initialSessions = yield* listSessions()
				const initialMap = new Map<string, TmuxStatus>()
				for (const session of initialSessions) {
					initialMap.set(session.beadId, session.status)
				}
				yield* Ref.set(previousStateRef, initialMap)

				// Log initial state
				if (initialSessions.length > 0) {
					yield* Effect.log(
						`TmuxSessionMonitor: Found ${initialSessions.length} active Claude sessions`,
					)
				}

				// Start polling fiber
				const pollerFiber = yield* Effect.gen(function* () {
					const sessions = yield* listSessions()
					const previousState = yield* Ref.get(previousStateRef)
					const newState = new Map<string, TmuxStatus>()

					for (const session of sessions) {
						newState.set(session.beadId, session.status)

						const prevStatus = previousState.get(session.beadId)
						if (prevStatus !== session.status) {
							// State changed - call handler
							yield* Effect.log(
								`TmuxSessionMonitor: ${session.beadId} status changed: ${prevStatus ?? "none"} → ${session.status}`,
							)
							yield* handler(session)
						}
					}

					// Check for sessions that disappeared (session ended)
					for (const beadId of previousState.keys()) {
						if (!newState.has(beadId)) {
							// Session disappeared - treat as idle
							// createdAt is 0 and paths are null since we can't query a dead session
							yield* Effect.log(`TmuxSessionMonitor: ${beadId} session ended`)
							yield* handler({
								beadId,
								status: "idle",
								sessionName: `${CLAUDE_SESSION_PREFIX}${beadId}`,
								createdAt: 0,
								worktreePath: null,
								projectPath: null,
							})
						}
					}

					yield* Ref.set(previousStateRef, newState)
				}).pipe(
					// Catch errors to prevent stopping
					Effect.catchAll((e) =>
						Effect.logWarning(`TmuxSessionMonitor poll error: ${e}`).pipe(Effect.asVoid),
					),
					// Repeat with polling interval
					Effect.repeat(Schedule.spaced(`${POLL_INTERVAL_MS} millis`)),
					Effect.forkScoped,
				)

				// Track the polling fiber in diagnostics
				yield* diagnostics.registerFiber({
					id: "tmux-session-monitor-poller",
					name: "TmuxSessionMonitor Poller",
					description: "Polls tmux sessions for Claude Code session state",
					fiber: pollerFiber,
				})

				return pollerFiber
			})

		return {
			start,
			getSessionStatus,
			getSessionCreatedAt,
			listSessions,
		}
	}),
}) {}
