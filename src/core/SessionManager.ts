/**
 * SessionManager - Effect service for Claude session orchestration
 *
 * Core orchestration service that manages the lifecycle of Claude Code sessions:
 * - Spawns Claude in tmux sessions
 * - Coordinates with WorktreeManager for isolated git environments
 * - Tracks session state using StateDetector for output pattern matching
 * - Publishes state change events via PubSub
 * - Maintains session registry in Ref<HashMap>
 *
 * Key features:
 * - start(beadId): Create worktree, tmux session, and launch Claude
 * - stop(beadId): Kill tmux session and cleanup
 * - pause(beadId): Send Ctrl+C and create WIP commit
 * - resume(beadId): Continue paused session
 * - getState(beadId): Get current session state
 * - listActive(): List all running sessions
 */

import { Command, type CommandExecutor } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Data, Effect, HashMap, Layer, PubSub, Ref } from "effect"
import { AppConfig } from "../config/index.js"
import type { SessionState } from "../ui/types.js"
import { BeadsClient, BeadsClientLive } from "./BeadsClient.js"
import { getSessionName } from "./paths.js"
import { StateDetector, StateDetectorLive } from "./StateDetector.js"
import {
	type TmuxError,
	TmuxService,
	TmuxServiceLive,
	type SessionNotFoundError as TmuxSessionNotFoundError,
} from "./TmuxService.js"
import {
	GitError,
	type NotAGitRepoError,
	WorktreeManager,
	WorktreeManagerLive,
} from "./WorktreeManager.js"
import type { BeadsError, NotFoundError, ParseError } from "./BeadsClient.js"

// ============================================================================
// Type Definitions
// ============================================================================

/**
 * Session information tracked by SessionManager
 */
export interface Session {
	readonly beadId: string
	readonly worktreePath: string
	readonly tmuxSessionName: string
	readonly state: SessionState
	readonly startedAt: Date
	readonly projectPath: string
}

/**
 * Options for starting a session
 */
export interface StartSessionOptions {
	readonly beadId: string
	readonly projectPath: string
	readonly baseBranch?: string
}

/**
 * State change event published to PubSub
 */
export interface SessionStateChange {
	readonly beadId: string
	readonly oldState: SessionState
	readonly newState: SessionState
	readonly timestamp: Date
}

// ============================================================================
// Error Types
// ============================================================================

/**
 * Generic session error
 */
export class SessionError extends Data.TaggedError("SessionError")<{
	readonly message: string
	readonly beadId?: string
}> {}

/**
 * Error when session is not found
 */
export class SessionNotFoundError extends Data.TaggedError("SessionNotFoundError")<{
	readonly beadId: string
}> {}

/**
 * Error when session already exists
 */
export class SessionExistsError extends Data.TaggedError("SessionExistsError")<{
	readonly beadId: string
}> {}

/**
 * Error when session is in invalid state for operation
 */
export class InvalidStateError extends Data.TaggedError("InvalidStateError")<{
	readonly beadId: string
	readonly currentState: SessionState
	readonly expectedState?: SessionState
	readonly operation: string
}> {}

// ============================================================================
// Service Definition using Effect.Service
// ============================================================================

/**
 * SessionManager service for Claude session orchestration
 *
 * Uses Effect.Service pattern for clean dependency injection.
 * Dependencies are declared explicitly and composed via layers.
 *
 * Generated layers:
 * - SessionManager.Default: Includes WorktreeManager, TmuxService, BeadsClient, StateDetector
 *   but still requires AppConfig (contextual, varies by project)
 * - SessionManager.DefaultWithoutDependencies: Requires all deps externally
 *
 * @example
 * ```ts
 * // Use Default layer with AppConfig provided separately
 * const program = Effect.gen(function* () {
 *   const manager = yield* SessionManager
 *   const session = yield* manager.start({ beadId: "az-05y", projectPath: "/project" })
 * }).pipe(
 *   Effect.provide(SessionManager.Default),
 *   Effect.provide(AppConfigLive)
 * )
 * ```
 */
export class SessionManager extends Effect.Service<SessionManager>()("SessionManager", {
	effect: Effect.gen(function* () {
		// Get dependencies
		const worktreeManager = yield* WorktreeManager
		const tmuxService = yield* TmuxService
		const beadsClient = yield* BeadsClient
		const _stateDetector = yield* StateDetector
		const { config: resolvedConfig } = yield* AppConfig

		// Track active sessions in memory
		const sessionsRef = yield* Ref.make<HashMap.HashMap<string, Session>>(HashMap.empty())

		// PubSub for state change events
		const stateChangeHub = yield* PubSub.unbounded<SessionStateChange>()

		// Helper: Publish state change event
		const publishStateChange = (
			beadId: string,
			oldState: SessionState,
			newState: SessionState,
		): Effect.Effect<void, never, never> =>
			PubSub.publish(stateChangeHub, {
				beadId,
				oldState,
				newState,
				timestamp: new Date(),
			}).pipe(
				Effect.asVoid,
				Effect.orElseSucceed(() => undefined),
			)

		return {
			/**
			 * Start a new Claude session for a bead
			 *
			 * Creates a git worktree, spawns a tmux session, and launches Claude Code.
			 * Idempotent: if session already exists, returns existing session.
			 */
			start: (
				options: StartSessionOptions,
			): Effect.Effect<
				Session,
				| SessionError
				| GitError
				| NotAGitRepoError
				| TmuxError
				| BeadsError
				| NotFoundError
				| ParseError,
				CommandExecutor.CommandExecutor
			> =>
				Effect.gen(function* () {
					const { beadId, projectPath, baseBranch } = options

					// Check if session already exists (idempotent)
					const sessions = yield* Ref.get(sessionsRef)
					const existingSession = HashMap.get(sessions, beadId)

					if (existingSession._tag === "Some") {
						return existingSession.value
					}

					// Get bead info to verify it exists
					yield* beadsClient.show(beadId)

					// Create worktree (idempotent - returns existing if present)
					const worktree = yield* worktreeManager.create({
						beadId,
						projectPath,
						baseBranch,
					})

					// Get configuration for init commands and session settings
					const worktreeConfig = resolvedConfig.worktree
					const sessionConfig = resolvedConfig.session

					// Run init commands after worktree creation (e.g., "direnv allow", "bun install")
					const { initCommands, env, continueOnFailure, parallel } = worktreeConfig
					if (initCommands.length > 0) {
						const runInitCommand = (cmd: string) =>
							Effect.gen(function* () {
								const initCmd = Command.make("sh", "-c", cmd).pipe(
									Command.workingDirectory(worktree.path),
									Command.env(env),
								)
								const exitCode = yield* Command.exitCode(initCmd).pipe(
									Effect.catchAll(() => Effect.succeed(1)),
								)
								if (exitCode !== 0) {
									yield* Effect.logWarning(`Init command failed: ${cmd}`)
									if (!continueOnFailure) {
										return yield* Effect.fail(
											new SessionError({
												message: `Init command failed: ${cmd}`,
												beadId,
											}),
										)
									}
								}
							})

						if (parallel) {
							// Run all commands in parallel
							yield* Effect.all(initCommands.map(runInitCommand), { concurrency: "unbounded" })
						} else {
							// Run commands sequentially (default)
							for (const cmd of initCommands) {
								yield* runInitCommand(cmd)
							}
						}
					}

					// Generate tmux session name
					const tmuxSessionName = getSessionName(beadId)

					// Check if tmux session already exists
					const hasSession = yield* tmuxService.hasSession(tmuxSessionName)

					if (!hasSession) {
						// Create tmux session in the worktree directory
						// Use user's shell that runs claude so:
						// 1. tmux prefix keys work (shell handles them, not claude)
						// 2. If claude exits, you're left in a shell (session doesn't die)
						const { command: claudeCommand, shell, tmuxPrefix, dangerouslySkipPermissions } =
							sessionConfig
						const claudeArgs = dangerouslySkipPermissions ? " --dangerously-skip-permissions" : ""
						yield* tmuxService.newSession(tmuxSessionName, {
							cwd: worktree.path,
							command: `${shell} -c '${claudeCommand}${claudeArgs}; exec ${shell}'`,
							prefix: tmuxPrefix,
						})
					}

					// Create session object
					const session: Session = {
						beadId,
						worktreePath: worktree.path,
						tmuxSessionName,
						state: "busy",
						startedAt: new Date(),
						projectPath,
					}

					// Store session in registry
					yield* Ref.update(sessionsRef, (sessions) => HashMap.set(sessions, beadId, session))

					// Publish state change event (from idle to busy)
					yield* publishStateChange(beadId, "idle", "busy")

					return session
				}),

			/**
			 * Stop a running session
			 *
			 * Kills the tmux session. Does not remove the worktree.
			 */
			stop: (
				beadId: string,
			): Effect.Effect<void, SessionError | TmuxError, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					const sessions = yield* Ref.get(sessionsRef)
					const sessionOpt = HashMap.get(sessions, beadId)

					if (sessionOpt._tag === "None") {
						return yield* Effect.fail(
							new SessionError({
								message: "Session not found",
								beadId,
							}),
						)
					}

					const session = sessionOpt.value

					// Sync beads changes from worktree before killing session
					yield* beadsClient.sync(session.worktreePath).pipe(
						Effect.catchAll(() => Effect.void),
					)

					// Kill tmux session (ignore error if already dead)
					yield* tmuxService
						.killSession(session.tmuxSessionName)
						.pipe(Effect.catchAll(() => Effect.void))

					// Get old state for event
					const oldState = session.state

					// Remove from registry
					yield* Ref.update(sessionsRef, (sessions) => HashMap.remove(sessions, beadId))

					// Publish state change event
					yield* publishStateChange(beadId, oldState, "idle")
				}),

			/**
			 * Pause a running session
			 *
			 * Sends Ctrl+C to interrupt Claude, then creates a WIP commit.
			 */
			pause: (
				beadId: string,
			): Effect.Effect<
				void,
				SessionError | TmuxSessionNotFoundError | TmuxError | GitError,
				CommandExecutor.CommandExecutor
			> =>
				Effect.gen(function* () {
					const sessions = yield* Ref.get(sessionsRef)
					const sessionOpt = HashMap.get(sessions, beadId)

					if (sessionOpt._tag === "None") {
						return yield* Effect.fail(
							new SessionError({
								message: "Session not found",
								beadId,
							}),
						)
					}

					const session = sessionOpt.value

					// Send Ctrl+C to interrupt Claude
					yield* tmuxService.sendKeys(session.tmuxSessionName, "C-c")

					// Wait a moment for interrupt to process
					yield* Effect.sleep("500 millis")

					// Sync beads changes from worktree
					yield* beadsClient.sync(session.worktreePath).pipe(
						Effect.catchAll(() => Effect.void),
					)

					// Create WIP commit in worktree
					const addCmd = Command.make("git", "add", "-A").pipe(
						Command.workingDirectory(session.worktreePath),
					)
					yield* Command.exitCode(addCmd).pipe(
						Effect.mapError(
							(e) =>
								new GitError({
									message: `Failed to stage changes: ${e}`,
									command: "git add -A",
								}),
						),
					)

					// Git commit with WIP message
					const commitCmd = Command.make("git", "commit", "-m", "WIP: Paused session").pipe(
						Command.workingDirectory(session.worktreePath),
					)
					yield* Command.exitCode(commitCmd).pipe(
						Effect.mapError(
							(e) =>
								new GitError({
									message: `Failed to create WIP commit: ${e}`,
									command: "git commit -m 'WIP: Paused session'",
								}),
						),
						Effect.catchAll(() => Effect.succeed(0)),
					)

					// Update session state to paused
					const oldState = session.state
					const updatedSession: Session = {
						...session,
						state: "paused",
					}

					yield* Ref.update(sessionsRef, (sessions) => HashMap.set(sessions, beadId, updatedSession))

					// Publish state change
					yield* publishStateChange(beadId, oldState, "paused")
				}),

			/**
			 * Resume a paused session
			 *
			 * Updates state to "busy" (user will manually reattach to tmux).
			 */
			resume: (beadId: string): Effect.Effect<void, SessionError | InvalidStateError, never> =>
				Effect.gen(function* () {
					const sessions = yield* Ref.get(sessionsRef)
					const sessionOpt = HashMap.get(sessions, beadId)

					if (sessionOpt._tag === "None") {
						return yield* Effect.fail(
							new SessionError({
								message: "Session not found",
								beadId,
							}),
						)
					}

					const session = sessionOpt.value

					// Verify session is paused
					if (session.state !== "paused") {
						return yield* Effect.fail(
							new InvalidStateError({
								beadId,
								currentState: session.state,
								expectedState: "paused",
								operation: "resume",
							}),
						)
					}

					// Update state to busy
					const updatedSession: Session = {
						...session,
						state: "busy",
					}

					yield* Ref.update(sessionsRef, (sessions) => HashMap.set(sessions, beadId, updatedSession))

					// Publish state change
					yield* publishStateChange(beadId, "paused", "busy")
				}),

			/**
			 * Get current state for a session
			 */
			getState: (beadId: string): Effect.Effect<SessionState, SessionNotFoundError, never> =>
				Effect.gen(function* () {
					const sessions = yield* Ref.get(sessionsRef)
					const sessionOpt = HashMap.get(sessions, beadId)

					if (sessionOpt._tag === "None") {
						return yield* Effect.fail(new SessionNotFoundError({ beadId }))
					}

					return sessionOpt.value.state
				}),

			/**
			 * List all active sessions
			 */
			listActive: (): Effect.Effect<Session[], never, never> =>
				Effect.gen(function* () {
					// Get in-memory sessions
					const inMemorySessions = yield* Ref.get(sessionsRef)

					// Query tmux for actual running sessions
					const tmuxSessions = yield* tmuxService.listSessions().pipe(
						Effect.catchAll(() => Effect.succeed([])),
					)

					// Find tmux sessions that look like bead IDs but aren't tracked
					const beadIdPattern = /^[a-z]+-[a-z0-9]+$/i

					for (const tmuxSession of tmuxSessions) {
						if (
							beadIdPattern.test(tmuxSession.name) &&
							!HashMap.has(inMemorySessions, tmuxSession.name)
						) {
							// This is an orphaned session - add it to tracking
							const orphanedSession: Session = {
								beadId: tmuxSession.name,
								worktreePath: "",
								tmuxSessionName: tmuxSession.name,
								state: "busy",
								startedAt: tmuxSession.created,
								projectPath: process.cwd(),
							}
							yield* Ref.update(sessionsRef, (sessions) =>
								HashMap.set(sessions, tmuxSession.name, orphanedSession),
							)
						}
					}

					// Return updated list
					const updatedSessions = yield* Ref.get(sessionsRef)
					return Array.from(HashMap.values(updatedSessions))
				}),

			/**
			 * Update session state
			 *
			 * Internal method for state updates. Publishes state change events.
			 */
			updateState: (
				beadId: string,
				newState: SessionState,
			): Effect.Effect<void, SessionNotFoundError, never> =>
				Effect.gen(function* () {
					const sessions = yield* Ref.get(sessionsRef)
					const sessionOpt = HashMap.get(sessions, beadId)

					if (sessionOpt._tag === "None") {
						return yield* Effect.fail(new SessionNotFoundError({ beadId }))
					}

					const session = sessionOpt.value
					const oldState = session.state

					const updatedSession: Session = {
						...session,
						state: newState,
					}

					yield* Ref.update(sessionsRef, (sessions) => HashMap.set(sessions, beadId, updatedSession))

					// Publish state change
					yield* publishStateChange(beadId, oldState, newState)
				}),

			/**
			 * Subscribe to state change events
			 *
			 * Returns a PubSub for subscribing to state changes.
			 */
			subscribeToStateChanges: (): Effect.Effect<PubSub.PubSub<SessionStateChange>, never, never> =>
				Effect.succeed(stateChangeHub),
		}
	}),

	// Dependencies bundled into SessionManager.Default
	// Note: AppConfig is NOT here - it's contextual (needs projectPath)
	// so SessionManager.Default still requires AppConfig to be provided
	dependencies: [
		WorktreeManagerLive,
		TmuxServiceLive,
		BeadsClientLive,
		StateDetectorLive,
		BunContext.layer,
	],
}) {}

// ============================================================================
// Legacy Exports for Backwards Compatibility
// ============================================================================

/**
 * @deprecated Use SessionManager.Default instead
 *
 * Note: SessionManager.Default requires AppConfig to be provided.
 * Use: SessionManager.Default.pipe(Layer.provide(AppConfigLiveWithPlatform(projectPath)))
 */
export const SessionManagerLive = SessionManager.Default

/**
 * @deprecated Use SessionManager.Default instead
 */
export const SessionManagerLiveWithPlatform = SessionManager.Default

// ============================================================================
// Convenience Functions
// ============================================================================

/**
 * Start a new Claude session
 */
export const start = (
	options: StartSessionOptions,
): Effect.Effect<
	Session,
	SessionError | GitError | NotAGitRepoError | TmuxError | BeadsError | NotFoundError | ParseError,
	SessionManager | CommandExecutor.CommandExecutor
> => Effect.flatMap(SessionManager, (manager) => manager.start(options))

/**
 * Stop a running session
 */
export const stop = (
	beadId: string,
): Effect.Effect<
	void,
	SessionError | TmuxError,
	SessionManager | CommandExecutor.CommandExecutor
> => Effect.flatMap(SessionManager, (manager) => manager.stop(beadId))

/**
 * Pause a running session
 */
export const pause = (
	beadId: string,
): Effect.Effect<
	void,
	SessionError | TmuxSessionNotFoundError | TmuxError | GitError,
	SessionManager | CommandExecutor.CommandExecutor
> => Effect.flatMap(SessionManager, (manager) => manager.pause(beadId))

/**
 * Resume a paused session
 */
export const resume = (
	beadId: string,
): Effect.Effect<void, SessionError | InvalidStateError, SessionManager> =>
	Effect.flatMap(SessionManager, (manager) => manager.resume(beadId))

/**
 * Get current session state
 */
export const getState = (
	beadId: string,
): Effect.Effect<SessionState, SessionNotFoundError, SessionManager> =>
	Effect.flatMap(SessionManager, (manager) => manager.getState(beadId))

/**
 * List all active sessions
 */
export const listActive = (): Effect.Effect<Session[], never, SessionManager> =>
	Effect.flatMap(SessionManager, (manager) => manager.listActive())

/**
 * Update session state
 */
export const updateState = (
	beadId: string,
	newState: SessionState,
): Effect.Effect<void, SessionNotFoundError, SessionManager> =>
	Effect.flatMap(SessionManager, (manager) => manager.updateState(beadId, newState))

/**
 * Subscribe to state change events
 */
export const subscribeToStateChanges = (): Effect.Effect<
	PubSub.PubSub<SessionStateChange>,
	never,
	SessionManager
> => Effect.flatMap(SessionManager, (manager) => manager.subscribeToStateChanges())
