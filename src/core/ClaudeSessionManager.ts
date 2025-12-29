/**
 * ClaudeSessionManager - Effect service for Claude session orchestration
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
import { BunContext } from "@effect/platform-bun"
import { Data, DateTime, Effect, Exit, HashMap, Option, PubSub, Ref, Schema } from "effect"
import { AppConfig } from "../config/index.js"
import { DiagnosticsService } from "../services/DiagnosticsService.js"
import { ProjectService } from "../services/ProjectService.js"
import type { SessionState } from "../ui/types.js"
import { BeadsClient, type BeadsError, type NotFoundError, type ParseError } from "./BeadsClient.js"
import { getToolDefinition } from "./CliToolRegistry.js"
import { getBeadSessionName, getWorktreePath, parseSessionName, WINDOW_NAMES } from "./paths.js"
import { StateDetector } from "./StateDetector.js"
import {
	type TmuxError,
	TmuxService,
	type SessionNotFoundError as TmuxSessionNotFoundError,
} from "./TmuxService.js"
import { GitError, type NotAGitRepoError, WorktreeManager } from "./WorktreeManager.js"
import { WorktreeSessionService } from "./WorktreeSessionService.js"

// ============================================================================
// Type Definitions
// ============================================================================

/**
 * Session information tracked by ClaudeSessionManager
 */
export interface Session {
	readonly beadId: string
	readonly worktreePath: string
	readonly tmuxSessionName: string
	readonly state: SessionState
	readonly startedAt: DateTime.Utc
	readonly projectPath: string
}

// ============================================================================
// Persistence Schema
// ============================================================================

/**
 * Schema for session state - validates against SessionState literals
 */
const SessionStateSchema = Schema.Literal(
	"idle",
	"initializing",
	"busy",
	"waiting",
	"done",
	"error",
	"paused",
	"warning",
)

/**
 * Schema for persisted session - matches Session interface
 * Schema.DateTimeUtc handles ISO string ↔ DateTime at JSON boundary
 */
const SessionSchema = Schema.Struct({
	beadId: Schema.String,
	worktreePath: Schema.String,
	tmuxSessionName: Schema.String,
	state: SessionStateSchema,
	startedAt: Schema.DateTimeUtc,
	projectPath: Schema.String,
})

/**
 * Claude model to use for session
 *
 * Supports short names for Claude (haiku, sonnet, opus) or
 * provider/model format for OpenCode (anthropic/claude-sonnet-20241022,
 * google/gemini-flash-1.5, etc.)
 */
export type ClaudeModel = string

/**
 * Options for starting a session
 */
export interface StartSessionOptions {
	readonly beadId: string
	readonly projectPath: string
	readonly baseBranch?: string
	/** Optional initial prompt to send to Claude on startup (e.g., "work on bead az-123") */
	readonly initialPrompt?: string
	/** Optional model to use (haiku, sonnet, opus). Uses Claude default if not specified. */
	readonly model?: ClaudeModel
	/** Run Claude with --dangerously-skip-permissions flag (default: false) */
	readonly dangerouslySkipPermissions?: boolean
	/** Enable auto-compact for long-running sessions (default: false, uses user setting) */
	readonly autoCompact?: boolean
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
 * ClaudeSessionManager service interface
 *
 * Provides typed access to Claude session orchestration with Effect error handling.
 * All operations compose WorktreeManager, TmuxService, BeadsClient, and StateDetector.
 */
export interface ClaudeSessionManagerService {
	/**
	 * Start a new Claude session for a bead
	 *
	 * Creates a git worktree, spawns a tmux session, and launches Claude Code.
	 * Idempotent: if session already exists, returns existing session.
	 *
	 * @example
	 * ```ts
	 * ClaudeSessionManager.start({
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
	 * ClaudeSessionManager.stop("az-05y")
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
	 * ClaudeSessionManager.pause("az-05y")
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
	 * ClaudeSessionManager.resume("az-05y")
	 * ```
	 */
	readonly resume: (beadId: string) => Effect.Effect<void, SessionError | InvalidStateError, never>

	/**
	 * Get current state for a session
	 *
	 * @example
	 * ```ts
	 * ClaudeSessionManager.getState("az-05y")
	 * ```
	 */
	readonly getState: (beadId: string) => Effect.Effect<SessionState, SessionNotFoundError, never>

	/**
	 * List all active sessions
	 *
	 * @example
	 * ```ts
	 * ClaudeSessionManager.listActive()
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
	 * Update session state from tmux status
	 *
	 * Handles mapping TmuxStatus to SessionState and handles
	 * secondary transitions like "done" detection.
	 *
	 * If the session doesn't exist but sessionMeta is provided,
	 * the session will be registered automatically (orphan recovery).
	 */
	readonly updateStateFromTmux: (
		beadId: string,
		status: "busy" | "waiting" | "idle",
		sessionMeta?: {
			sessionName: string
			createdAt: number
			worktreePath: string | null
			projectPath: string | null
		},
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
 * ClaudeSessionManager service
 *
 * Creates a service implementation with stateful session tracking via Ref<HashMap>.
 * Composes WorktreeManager, TmuxService, BeadsClient, and StateDetector services.
 *
 * @example
 * ```ts
 * const program = Effect.gen(function* () {
 *   const manager = yield* ClaudeSessionManager
 *   const session = yield* manager.start({
 *     beadId: "az-123",
 *     projectPath: process.cwd()
 *   })
 *   return session
 * }).pipe(Effect.provide(ClaudeSessionManager.Default))
 * ```
 */
export class ClaudeSessionManager extends Effect.Service<ClaudeSessionManager>()(
	"ClaudeSessionManager",
	{
		dependencies: [
			WorktreeManager.Default,
			TmuxService.Default,
			BeadsClient.Default,
			AppConfig.Default,
			StateDetector.Default,
			ProjectService.Default,
			DiagnosticsService.Default,
			WorktreeSessionService.Default,
		],
		effect: Effect.gen(function* () {
			// Get dependencies
			const worktreeManager = yield* WorktreeManager
			const tmuxService = yield* TmuxService
			const worktreeSession = yield* WorktreeSessionService
			const beadsClient = yield* BeadsClient
			const appConfig = yield* AppConfig
			const projectService = yield* ProjectService
			const diagnostics = yield* DiagnosticsService

			// Note: ClaudeSessionManager uses effect: not scoped:, so trackService (which uses acquireRelease)
			// would need scoped. Instead we just update health status manually.
			yield* diagnostics.updateServiceHealth({
				name: "ClaudeSessionManager",
				status: "healthy",
				details: "Claude session orchestration",
			})

			// Track active sessions in memory
			const sessionsRef = yield* Ref.make<HashMap.HashMap<string, Session>>(HashMap.empty())

			// PubSub for state change events
			const stateChangeHub = yield* PubSub.unbounded<SessionStateChange>()

			// ====================================================================
			// Session Persistence
			// ====================================================================

			// Schema handles ALL conversions:
			// - JSON string ↔ array of tuples (Schema.parseJson)
			// - Array of tuples ↔ HashMap (Schema.HashMap)
			// - ISO string ↔ DateTime (Schema.DateTimeUtc)
			const sessionFilePath = ".azedarach/sessions.json"
			const SessionsSchema = Schema.parseJson(
				Schema.HashMap({ key: Schema.String, value: SessionSchema }),
			)
			const decodeSessions = Schema.decodeUnknown(SessionsSchema)
			const encodeSessions = Schema.encode(SessionsSchema)

			// Layer for filesystem operations - provides FileSystem and Path services
			const fsLayer = BunContext.layer

			/**
			 * Get the current project path from ProjectService, falling back to process.cwd()
			 */
			const getEffectiveProjectPath = (): Effect.Effect<string> =>
				Effect.gen(function* () {
					const projectPath = yield* projectService.getCurrentPath()
					return projectPath ?? process.cwd()
				})

			// Helper: Load persisted sessions from disk
			const loadPersistedSessions = Effect.gen(function* () {
				const fs = yield* FileSystem.FileSystem
				const pathSvc = yield* Path.Path
				const projectPath = yield* getEffectiveProjectPath()
				const filePath = pathSvc.join(projectPath, sessionFilePath)

				const exists = yield* fs.exists(filePath)
				if (!exists) return HashMap.empty<string, Session>()

				const content = yield* fs.readFileString(filePath)
				return yield* decodeSessions(content)
			}).pipe(
				Effect.provide(fsLayer),
				Effect.catchAll(() => Effect.succeed(HashMap.empty<string, Session>())),
			)

			// Helper: Save sessions to disk
			const persistSessions = (sessions: HashMap.HashMap<string, Session>) =>
				Effect.gen(function* () {
					const fs = yield* FileSystem.FileSystem
					const pathSvc = yield* Path.Path
					const projectPath = yield* getEffectiveProjectPath()
					const dirPath = pathSvc.join(projectPath, ".azedarach")
					const filePath = pathSvc.join(dirPath, "sessions.json")

					yield* fs.makeDirectory(dirPath, { recursive: true }).pipe(Effect.ignore)
					const json = yield* encodeSessions(sessions)
					yield* fs.writeFileString(filePath, json).pipe(Effect.ignore)
				}).pipe(Effect.provide(fsLayer))

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
						const {
							beadId,
							projectPath,
							baseBranch: explicitBaseBranch,
							initialPrompt,
							model,
							dangerouslySkipPermissions,
							autoCompact,
						} = options

						// Check if session already exists (idempotent)
						const sessions = yield* Ref.get(sessionsRef)
						const existingSession = HashMap.get(sessions, beadId)

						if (existingSession._tag === "Some") {
							return existingSession.value
						}

						// Verify bead exists (will throw NotFoundError if not)
						const issue = yield* beadsClient.show(beadId)

						// Note: We update bead status to in_progress AFTER session creation succeeds
						// to avoid the bug where status updates but session fails (az-g7p)
						const needsStatusUpdate = issue.status !== "in_progress"

						// Determine effective base branch:
						// 1. If explicit baseBranch passed, use it
						// 2. If bead has a parent epic, use the epic branch
						// 3. Otherwise, use the default (WorktreeManager uses current branch)
						let effectiveBaseBranch = explicitBaseBranch

						if (!effectiveBaseBranch) {
							// Check if this bead has a parent epic
							const parentEpic = yield* beadsClient.getParentEpic(beadId)

							if (parentEpic) {
								// Ensure epic branch exists by creating epic worktree if needed
								// This is idempotent - if worktree already exists, it returns the existing one
								yield* worktreeManager.create({
									beadId: parentEpic.id,
									projectPath,
									// Epic branches from main (no baseBranch = uses current branch)
								})
								// Use the epic branch as base for the child task
								effectiveBaseBranch = parentEpic.id
								yield* Effect.log(`Child task ${beadId} will branch from epic ${parentEpic.id}`)
							}
						}

						// Create worktree (idempotent - returns existing if present)
						const worktree = yield* worktreeManager.create({
							beadId,
							projectPath,
							baseBranch: effectiveBaseBranch,
						})

						// NOTE: .claude/ directory is git-tracked so it's already in the worktree.
						// WorktreeManager.copyClaudeLocalSettings handles settings.local.json (gitignored).
						// No additional copying needed here.

						// Get session, worktree, CLI tool, and model config from current project
						const sessionConfig = yield* appConfig.getSessionConfig()
						const worktreeConfig = yield* appConfig.getWorktreeConfig()
						const cliTool = yield* appConfig.getCliTool()
						const modelConfig = yield* appConfig.getModelConfig()

						// DEBUG: Log which CLI tool is being used
						yield* Effect.log(`[DEBUG] cliTool from config: ${cliTool}`)

						// Get the tool definition for command building
						const toolDef = getToolDefinition(cliTool)

						// Generate tmux session name (just the beadId)
						const tmuxSessionName = getBeadSessionName(beadId)

						// Check if bead session already exists
						const hasSession = yield* tmuxService.hasSession(tmuxSessionName)

						// Build session settings object (for tools that support it, like Claude)
						const sessionSettings: Record<string, unknown> = {}
						if (autoCompact) sessionSettings.autoCompactEnabled = true

						// Determine which model to use:
						// 1. Explicitly passed model (from StartSessionOptions)
						// 2. Config model.[cliTool].default
						// 3. Config model.default
						// 4. Tool's default (undefined = let tool decide)
						const toolModelConfig = cliTool === "claude" ? modelConfig.claude : modelConfig.opencode
						const effectiveModel = model ?? toolModelConfig.default ?? modelConfig.default

						// Build command using the CLI tool registry
						const commandWithOptions = toolDef.buildCommand({
							initialPrompt,
							model: effectiveModel,
							dangerouslySkipPermissions,
							sessionSettings,
						})

						// Get initCommands: merge worktree config + tool-specific init commands
						const toolInitCommands = toolDef.getInitCommands()
						const initCommands = [...worktreeConfig.initCommands, ...toolInitCommands]
						const { tmuxPrefix, backgroundTasks } = sessionConfig

						// Use acquireUseRelease to ensure atomicity:
						// - acquire: Create tmux session + update bead status (both are "resources")
						// - use: Register session in memory + publish event
						// - release: Rollback tmux + bead status on failure
						//
						// This fixes az-losz: if any step fails after tmux creation or bead update,
						// we roll back ALL changes to avoid inconsistent state.
						const session = yield* Effect.acquireUseRelease(
							// ACQUIRE: Create tmux session and update bead status
							// Both are "resources" that need rollback on failure
							Effect.gen(function* () {
								let createdNewSession = false
								let updatedBeadStatus = false

								if (!hasSession) {
									yield* worktreeSession.getOrCreateSession(beadId, {
										worktreePath: worktree.path,
										projectPath,
										initCommands,
										tmuxPrefix,
										backgroundTasks,
									})
									createdNewSession = true
								}

								yield* worktreeSession.ensureWindow(tmuxSessionName, WINDOW_NAMES.CODE, {
									command: commandWithOptions,
									cwd: worktree.path,
								})

								// Step 2: Update bead status to in_progress
								// Done AFTER session creation to ensure we don't leave beads
								// in "in_progress" state with no actual session (az-g7p bug fix)
								if (needsStatusUpdate) {
									yield* beadsClient.update(beadId, { status: "in_progress" })
									updatedBeadStatus = true
								}

								return { createdNewSession, updatedBeadStatus }
							}),

							// USE: Register session in memory and publish event
							() =>
								Effect.gen(function* () {
									// Session starts as "initializing" - init commands and Claude are now chained
									// in the tmux session, so if init fails, Claude won't start
									const initialState: SessionState = "initializing"

									// Create session object
									const sessionObj: Session = {
										beadId,
										worktreePath: worktree.path,
										tmuxSessionName,
										state: initialState,
										startedAt: yield* DateTime.now,
										projectPath,
									}

									// Store session in registry
									yield* Ref.update(sessionsRef, (sessions) =>
										HashMap.set(sessions, beadId, sessionObj),
									)

									// Persist to disk
									const sessions = yield* Ref.get(sessionsRef)
									yield* persistSessions(sessions)

									// Publish state change event (from idle to initial state)
									yield* publishStateChange(beadId, "idle", initialState)

									return sessionObj
								}),

							// RELEASE: Rollback on failure - kill tmux and revert bead status
							(acquired, exit) =>
								Exit.isFailure(exit)
									? Effect.gen(function* () {
											// Rollback tmux session if we created it
											if (acquired.createdNewSession) {
												yield* tmuxService.killSession(tmuxSessionName).pipe(
													Effect.tap(() =>
														Effect.logWarning(`Rolled back tmux session ${tmuxSessionName}`),
													),
													Effect.catchAll(() => Effect.void),
												)
											}

											// Rollback bead status if we changed it
											if (acquired.updatedBeadStatus) {
												yield* beadsClient.update(beadId, { status: "open" }).pipe(
													Effect.tap(() =>
														Effect.logWarning(`Rolled back bead ${beadId} status to open`),
													),
													Effect.catchAll(() => Effect.void),
												)
											}
										})
									: Effect.void,
						)

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

						// Persist to disk
						const updatedSessions = yield* Ref.get(sessionsRef)
						yield* persistSessions(updatedSessions)

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

						// Persist to disk
						const allSessions = yield* Ref.get(sessionsRef)
						yield* persistSessions(allSessions)

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

						// Persist to disk
						const allSessions = yield* Ref.get(sessionsRef)
						yield* persistSessions(allSessions)

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

				listActive: (projectPath?: string) =>
					Effect.gen(function* () {
						// Get in-memory sessions
						const inMemorySessions = yield* Ref.get(sessionsRef)

						// Query tmux for actual running sessions
						const tmuxSessions = yield* tmuxService.listSessions().pipe(
							Effect.catchAll(() => Effect.succeed([])), // If tmux fails, just use in-memory
						)

						// Load persisted sessions for state recovery
						const persistedSessions = yield* loadPersistedSessions

						// Query worktrees to get accurate paths
						// Falls back to process.cwd() for backwards compatibility
						const effectiveProjectPath = projectPath ?? process.cwd()
						const worktrees = yield* worktreeManager
							.list(effectiveProjectPath)
							.pipe(Effect.catchAll(() => Effect.succeed([])))
						const worktreeByBeadId = HashMap.fromIterable(
							worktrees.map((wt) => [wt.beadId, wt] as const),
						)

						for (const tmuxSession of tmuxSessions) {
							const parsed = parseSessionName(tmuxSession.name)
							if (!parsed || parsed.type !== "bead") continue

							const beadId = parsed.beadId

							if (HashMap.has(inMemorySessions, beadId)) continue

							{
								const worktreeOpt = HashMap.get(worktreeByBeadId, beadId)
								const persistedOpt = HashMap.get(persistedSessions, beadId)

								const orphanedSession: Session = {
									beadId,
									worktreePath: Option.getOrElse(
										Option.map(worktreeOpt, (wt) => wt.path),
										() =>
											Option.getOrElse(
												Option.map(persistedOpt, (p) => p.worktreePath),
												() => getWorktreePath(effectiveProjectPath, beadId),
											),
									),
									tmuxSessionName: tmuxSession.name,
									state: Option.getOrElse(
										Option.map(persistedOpt, (p) => p.state),
										() => "busy",
									),
									startedAt: Option.getOrElse(
										Option.map(persistedOpt, (p) => p.startedAt),
										() => DateTime.unsafeFromDate(tmuxSession.created),
									),
									projectPath: Option.getOrElse(
										Option.map(persistedOpt, (p) => p.projectPath),
										() => effectiveProjectPath,
									),
								}
								yield* Ref.update(sessionsRef, (sessions) =>
									HashMap.set(sessions, beadId, orphanedSession),
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

						// Persist to disk
						const allSessions = yield* Ref.get(sessionsRef)
						yield* persistSessions(allSessions)

						// Publish state change
						yield* publishStateChange(beadId, oldState, newState)
					}),

				updateStateFromTmux: (
					beadId: string,
					status: "busy" | "waiting" | "idle",
					sessionMeta?: {
						sessionName: string
						createdAt: number
						worktreePath: string | null
						projectPath: string | null
					},
				) =>
					Effect.gen(function* () {
						const sessions = yield* Ref.get(sessionsRef)
						const sessionOpt = HashMap.get(sessions, beadId)

						// If session doesn't exist but we have metadata, create it (orphan recovery)
						if (sessionOpt._tag === "None") {
							if (sessionMeta) {
								yield* Effect.log(`Recovering orphaned session for ${beadId} (status: ${status})`)

								// Map status to SessionState
								let initialState: SessionState = "busy"
								if (status === "waiting") initialState = "waiting"
								if (status === "idle") initialState = "idle"

								const orphanedSession: Session = {
									beadId,
									worktreePath:
										sessionMeta.worktreePath ??
										getWorktreePath(sessionMeta.projectPath ?? process.cwd(), beadId),
									tmuxSessionName: sessionMeta.sessionName,
									state: initialState,
									startedAt: DateTime.unsafeFromDate(new Date(sessionMeta.createdAt * 1000)),
									projectPath: sessionMeta.projectPath ?? process.cwd(),
								}

								yield* Ref.update(sessionsRef, (sessions) =>
									HashMap.set(sessions, beadId, orphanedSession),
								)

								// Persist the recovered session
								const allSessions = yield* Ref.get(sessionsRef)
								yield* persistSessions(allSessions)

								// Publish as a new session discovery (idle -> currentState)
								yield* publishStateChange(beadId, "idle", initialState)
								return
							}
							return yield* Effect.fail(new SessionNotFoundError({ beadId }))
						}

						const session = sessionOpt.value
						const oldState = session.state

						// Map TmuxStatus to SessionState
						let newState: SessionState = session.state
						if (status === "busy") newState = "busy"
						if (status === "waiting") newState = "waiting"
						if (status === "idle") {
							// If we were busy or waiting and session disappeared, it might be "done"
							// but for now we'll just map to idle. Transition to "done"
							// is usually handled by output pattern matching in PTYMonitor
							// or explicit az notify done.
							newState = "idle"
						}

						if (oldState === newState) return

						const updatedSession: Session = {
							...session,
							state: newState,
						}

						yield* Ref.update(sessionsRef, (sessions) =>
							HashMap.set(sessions, beadId, updatedSession),
						)

						// Persist to disk
						const allSessions = yield* Ref.get(sessionsRef)
						yield* persistSessions(allSessions)

						// Publish state change
						yield* publishStateChange(beadId, oldState, newState)
					}),

				subscribeToStateChanges: () => Effect.succeed(stateChangeHub),
			}
		}),
	},
) {}
