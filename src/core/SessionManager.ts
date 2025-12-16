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

import { Command, type CommandExecutor, FileSystem, Path } from "@effect/platform"
import { Data, Effect, HashMap, PubSub, Ref } from "effect"
import { AppConfig, type ResolvedConfig } from "../config/index.js"
import type { SessionState } from "../ui/types.js"
import { BeadsClient, type BeadsError, type NotFoundError, type ParseError } from "./BeadsClient.js"
import { getSessionName } from "./paths.js"
import { StateDetector } from "./StateDetector.js"
import {
	type TmuxError,
	TmuxService,
	type SessionNotFoundError as TmuxSessionNotFoundError,
} from "./TmuxService.js"
import { GitError, type NotAGitRepoError, WorktreeManager } from "./WorktreeManager.js"

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
	/** Optional initial prompt to send to Claude on startup (e.g., "work on bead az-123") */
	readonly initialPrompt?: string
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
// Service Definition
// ============================================================================

/**
 * SessionManager service interface
 *
 * Provides typed access to Claude session orchestration with Effect error handling.
 * All operations compose WorktreeManager, TmuxService, BeadsClient, and StateDetector.
 */
export interface SessionManagerService {
	/**
	 * Start a new Claude session for a bead
	 *
	 * Creates a git worktree, spawns a tmux session, and launches Claude Code.
	 * Idempotent: if session already exists, returns existing session.
	 *
	 * @example
	 * ```ts
	 * SessionManager.start({
	 *   beadId: "az-05y",
	 *   projectPath: "/Users/user/project",
	 *   baseBranch: "main"
	 * })
	 * ```
	 */
	readonly start: (
		options: StartSessionOptions,
	) => Effect.Effect<
		Session,
		| SessionError
		| GitError
		| NotAGitRepoError
		| TmuxError
		| BeadsError
		| NotFoundError
		| ParseError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Stop a running session
	 *
	 * Kills the tmux session. Does not remove the worktree (use WorktreeManager.remove separately).
	 *
	 * @example
	 * ```ts
	 * SessionManager.stop("az-05y")
	 * ```
	 */
	readonly stop: (
		beadId: string,
	) => Effect.Effect<void, SessionError | TmuxError, CommandExecutor.CommandExecutor>

	/**
	 * Pause a running session
	 *
	 * Sends Ctrl+C to the tmux session to interrupt Claude, then creates a WIP commit.
	 * Updates session state to "paused".
	 *
	 * @example
	 * ```ts
	 * SessionManager.pause("az-05y")
	 * ```
	 */
	readonly pause: (
		beadId: string,
	) => Effect.Effect<
		void,
		SessionError | TmuxSessionNotFoundError | TmuxError | GitError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Resume a paused session
	 *
	 * Reattaches to the tmux session and updates state to "busy".
	 *
	 * @example
	 * ```ts
	 * SessionManager.resume("az-05y")
	 * ```
	 */
	readonly resume: (beadId: string) => Effect.Effect<void, SessionError | InvalidStateError, never>

	/**
	 * Get current state for a session
	 *
	 * @example
	 * ```ts
	 * SessionManager.getState("az-05y")
	 * ```
	 */
	readonly getState: (beadId: string) => Effect.Effect<SessionState, SessionNotFoundError, never>

	/**
	 * List all active sessions
	 *
	 * @example
	 * ```ts
	 * SessionManager.listActive()
	 * ```
	 */
	readonly listActive: () => Effect.Effect<Session[], never, never>

	/**
	 * Update session state
	 *
	 * Internal method for state updates. Publishes state change events.
	 */
	readonly updateState: (
		beadId: string,
		newState: SessionState,
	) => Effect.Effect<void, SessionNotFoundError, never>

	/**
	 * Subscribe to state change events
	 *
	 * Returns a stream of SessionStateChange events.
	 */
	readonly subscribeToStateChanges: () => Effect.Effect<
		PubSub.PubSub<SessionStateChange>,
		never,
		never
	>
}

// ============================================================================
// Service Implementation
// ============================================================================

/**
 * SessionManager service
 *
 * Creates a service implementation with stateful session tracking via Ref<HashMap>.
 * Composes WorktreeManager, TmuxService, BeadsClient, and StateDetector services.
 *
 * @example
 * ```ts
 * const program = Effect.gen(function* () {
 *   const manager = yield* SessionManager
 *   const session = yield* manager.start({
 *     beadId: "az-123",
 *     projectPath: process.cwd()
 *   })
 *   return session
 * }).pipe(Effect.provide(SessionManager.Default))
 * ```
 */
export class SessionManager extends Effect.Service<SessionManager>()("SessionManager", {
	dependencies: [
		WorktreeManager.Default,
		TmuxService.Default,
		BeadsClient.Default,
		AppConfig.Default,
		StateDetector.Default,
	],
	effect: Effect.gen(function* () {
		// Get dependencies
		const worktreeManager = yield* WorktreeManager
		const tmuxService = yield* TmuxService
		const beadsClient = yield* BeadsClient
		const appConfig = yield* AppConfig
		const resolvedConfig: ResolvedConfig = appConfig.config

		// Get platform services at construction time (for use in closures)
		const fs = yield* FileSystem.FileSystem
		const pathService = yield* Path.Path

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
			start: (options: StartSessionOptions) =>
				Effect.gen(function* () {
					const { beadId, projectPath, baseBranch, initialPrompt } = options

					// Check if session already exists (idempotent)
					const sessions = yield* Ref.get(sessionsRef)
					const existingSession = HashMap.get(sessions, beadId)

					if (existingSession._tag === "Some") {
						return existingSession.value
					}

					// Get bead info to verify it exists
					const issue = yield* beadsClient.show(beadId)

					// Auto-update bead status to in_progress if not already
					// This ensures consistency: an active session implies active work
					if (issue.status !== "in_progress") {
						yield* beadsClient.update(beadId, { status: "in_progress" })
					}

					// Create worktree (idempotent - returns existing if present)
					const worktree = yield* worktreeManager.create({
						beadId,
						projectPath,
						baseBranch,
					})

					// NOTE: .claude/ directory is git-tracked so it's already in the worktree.
					// WorktreeManager.copyClaudeLocalSettings handles settings.local.json (gitignored).
					// No additional copying needed here.

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
						const { command: claudeCommand, shell, tmuxPrefix } = sessionConfig

						// Check if .envrc exists - if so, wrap command with direnv exec
						// This ensures the environment is properly loaded before Claude starts
						// (direnv shell hooks don't fire with `bash -c`)
						const envrcPath = pathService.join(worktree.path, ".envrc")
						const hasEnvrc = yield* fs
							.exists(envrcPath)
							.pipe(Effect.catchAll(() => Effect.succeed(false)))

						// Wrap with direnv exec if .envrc exists
						// If initialPrompt provided, append it to the claude command (properly escaped)
						const escapeForShell = (s: string) =>
							s.replace(/\\/g, "\\\\").replace(/"/g, '\\"').replace(/\$/g, "\\$")

						const claudeWithPrompt = initialPrompt
							? `${claudeCommand} "${escapeForShell(initialPrompt)}"`
							: claudeCommand
						const effectiveCommand = hasEnvrc
							? `direnv exec . ${claudeWithPrompt}`
							: claudeWithPrompt

						yield* tmuxService.newSession(tmuxSessionName, {
							cwd: worktree.path,
							command: `${shell} -c '${effectiveCommand}; exec ${shell}'`,
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

			stop: (beadId: string) =>
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

					yield* Effect.log(`Stopping session for ${beadId}`)

					// Sync beads changes from worktree before killing session
					// This ensures any bd update/close commands run in the worktree get synced back to main
					yield* beadsClient.sync(session.worktreePath).pipe(
						Effect.tap(() => Effect.log(`Synced beads from worktree for ${beadId}`)),
						Effect.catchAll((error) =>
							Effect.logWarning(`Sync failed for ${beadId}: ${error}`).pipe(Effect.asVoid),
						),
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

					yield* Effect.log(`Session stopped for ${beadId} (was: ${oldState})`)
				}),

			pause: (beadId: string) =>
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

					// Sync beads changes from worktree before creating WIP commit
					// This ensures any bd update/close commands are synced before we pause
					yield* beadsClient.sync(session.worktreePath).pipe(
						Effect.catchAll(() => Effect.void), // Ignore sync errors (non-critical)
					)

					// Create WIP commit in worktree
					// Git add all changes (including synced .beads/ directory)
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
						// Ignore error if nothing to commit
						Effect.catchAll(() => Effect.succeed(0)),
					)

					// Update session state to paused
					const oldState = session.state
					const updatedSession: Session = {
						...session,
						state: "paused",
					}

					yield* Ref.update(sessionsRef, (sessions) =>
						HashMap.set(sessions, beadId, updatedSession),
					)

					// Publish state change
					yield* publishStateChange(beadId, oldState, "paused")
				}),

			resume: (beadId: string) =>
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

					// Update state to busy (user will manually reattach to tmux)
					const updatedSession: Session = {
						...session,
						state: "busy",
					}

					yield* Ref.update(sessionsRef, (sessions) =>
						HashMap.set(sessions, beadId, updatedSession),
					)

					// Publish state change
					yield* publishStateChange(beadId, "paused", "busy")
				}),

			getState: (beadId: string) =>
				Effect.gen(function* () {
					const sessions = yield* Ref.get(sessionsRef)
					const sessionOpt = HashMap.get(sessions, beadId)

					if (sessionOpt._tag === "None") {
						return yield* Effect.fail(new SessionNotFoundError({ beadId }))
					}

					return sessionOpt.value.state
				}),

			listActive: () =>
				Effect.gen(function* () {
					// Get in-memory sessions
					const inMemorySessions = yield* Ref.get(sessionsRef)

					// Query tmux for actual running sessions
					const tmuxSessions = yield* tmuxService.listSessions().pipe(
						Effect.catchAll(() => Effect.succeed([])), // If tmux fails, just use in-memory
					)

					// Find tmux sessions that look like bead IDs (az-xxx pattern) but aren't tracked in memory
					// Our session names are just the bead ID (see getSessionName in paths.ts)
					const beadIdPattern = /^[a-z]+-[a-z0-9]+$/i

					for (const tmuxSession of tmuxSessions) {
						// Check if this looks like a bead ID and isn't already tracked
						if (
							beadIdPattern.test(tmuxSession.name) &&
							!HashMap.has(inMemorySessions, tmuxSession.name)
						) {
							// This is an orphaned session - add it to our tracking as "busy"
							const orphanedSession: Session = {
								beadId: tmuxSession.name,
								worktreePath: "", // Unknown - would need to query worktree
								tmuxSessionName: tmuxSession.name,
								state: "busy",
								startedAt: tmuxSession.created,
								projectPath: process.cwd(), // Assume current project
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

			updateState: (beadId: string, newState: SessionState) =>
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

					yield* Ref.update(sessionsRef, (sessions) =>
						HashMap.set(sessions, beadId, updatedSession),
					)

					// Publish state change
					yield* publishStateChange(beadId, oldState, newState)
				}),

			subscribeToStateChanges: () => Effect.succeed(stateChangeHub),
		}
	}),
}) {}
