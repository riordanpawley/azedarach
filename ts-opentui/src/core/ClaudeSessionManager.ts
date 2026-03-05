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
 * - start(issueId): Create worktree, tmux session, and launch Claude
 * - stop(issueId): Kill tmux session and cleanup
 * - pause(issueId): Send Ctrl+C and create WIP commit
 * - resume(issueId): Continue paused session
 * - getState(issueId): Get current session state
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
import {
	getIssueSessionName,
	getWorktreePath,
	normalizeIssueIdForLookup,
	parseIssueSessionName,
	WINDOW_NAMES,
} from "./paths.js"
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
	readonly issueId: string
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
	"crashed",
)

/**
 * Schema for persisted session - matches Session interface
 * Schema.DateTimeUtc handles ISO string ↔ DateTime at JSON boundary
 */
const SessionSchema = Schema.Struct({
	issueId: Schema.String,
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
	readonly issueId: string
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
	readonly issueId: string
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
	readonly issueId?: string
}> {}

/**
 * Error when session is not found
 */
export class SessionNotFoundError extends Data.TaggedError("SessionNotFoundError")<{
	readonly issueId: string
}> {}

/**
 * Error when session already exists
 */
export class SessionExistsError extends Data.TaggedError("SessionExistsError")<{
	readonly issueId: string
}> {}

/**
 * Error when maximum session limit is reached
 */
export class SessionLimitError extends Data.TaggedError("SessionLimitError")<{
	readonly message: string
	readonly limit: number
	readonly current: number
}> {}

/**
 * Error when session is in invalid state for operation
 */
export class InvalidStateError extends Data.TaggedError("InvalidStateError")<{
	readonly issueId: string
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
	 *   issueId: "az-05y",
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
		| ParseError
		| SessionLimitError,
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
		issueId: string,
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
		issueId: string,
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
	readonly resume: (issueId: string) => Effect.Effect<void, SessionError | InvalidStateError, never>

	/**
	 * Recover a crashed session
	 *
	 * Respawns a session whose tmux died (e.g., computer restart).
	 * Uses `claude --resume` to continue the conversation where it left off.
	 *
	 * @example
	 * ```ts
	 * ClaudeSessionManager.recoverSession("az-05y")
	 * ```
	 */
	readonly recoverSession: (
		issueId: string,
	) => Effect.Effect<
		Session,
		SessionNotFoundError | InvalidStateError | TmuxError | SessionError | SessionLimitError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Get current state for a session
	 *
	 * @example
	 * ```ts
	 * ClaudeSessionManager.getState("az-05y")
	 * ```
	 */
	readonly getState: (issueId: string) => Effect.Effect<SessionState, SessionNotFoundError, never>

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
		issueId: string,
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
		issueId: string,
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
 *     issueId: "az-123",
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
			const issueTrackerClient = yield* BeadsClient
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
				issueId: string,
				oldState: SessionState,
				newState: SessionState,
			): Effect.Effect<void, never, never> =>
				PubSub.publish(stateChangeHub, {
					issueId,
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
							issueId,
							projectPath,
							baseBranch: explicitBaseBranch,
							initialPrompt,
							model,
							dangerouslySkipPermissions,
							autoCompact,
						} = options

						// Check if session already exists AND is active (idempotent for active sessions)
						// If a session exists but is "idle", it means the tmux process died but we still
						// have a stale entry in memory. In that case, we should create a new session.
						const sessions = yield* Ref.get(sessionsRef)
						const existingSession = HashMap.get(sessions, issueId)

						if (existingSession._tag === "Some" && existingSession.value.state !== "idle") {
							return existingSession.value
						}

						// Check session limit
						const configForLimit = yield* appConfig.getSessionConfig()
						// Cast to any to access the new field since TS might not pick up the schema change immediately in this context
						// In a real build, the schema change should propagate types correctly
						const maxSessions = (configForLimit as any).maxSessions ?? 10
						const activeSessions = HashMap.reduce(sessions, 0, (count, session) =>
							session.state !== "idle" && session.state !== "crashed" ? count + 1 : count,
						)

						if (activeSessions >= maxSessions) {
							return yield* Effect.fail(
								new SessionLimitError({
									message: `Cannot start new session: Maximum session limit (${maxSessions}) reached.`,
									limit: maxSessions,
									current: activeSessions,
								}),
							)
						}

						// Verify bead exists (will throw NotFoundError if not)
						const issue = yield* issueTrackerClient.show(issueId)

						// Note: We update bead status to in_progress AFTER session creation succeeds
						// to avoid the bug where status updates but session fails (az-g7p)
						const needsStatusUpdate = issue.status !== "in_progress"

						// Determine effective base branch:
						// 1. If explicit baseBranch passed, use it
						// 2. If bead has a parent epic, use the epic branch
						// 3. Otherwise, use the default (WorktreeManager uses current branch)
						// Get worktree and hooks config early - we need copyPaths and preCompactEnabled for worktree creation
						const worktreeConfig = yield* appConfig.getWorktreeConfig()
						const hooksConfig = yield* appConfig.getHooksConfig()

						let effectiveBaseBranch = explicitBaseBranch
						let epicWorktreePath: string | undefined

						if (!effectiveBaseBranch) {
							// Check if this bead has a parent epic
							const parentEpic = yield* issueTrackerClient.getParentEpic(issueId)

							if (parentEpic) {
								// Ensure epic branch exists by creating epic worktree if needed
								// This is idempotent - if worktree already exists, it returns the existing one
								const epicWorktree = yield* worktreeManager.create({
									issueId: parentEpic.id,
									projectPath,
									// Epic branches from main (no baseBranch = uses current branch)
									// Epic gets copyPaths from config (copies from main project)
									copyPaths: worktreeConfig.copyPaths,
									preCompactEnabled: hooksConfig.preCompact.enabled,
								})
								// Use the epic branch as base for the child task
								effectiveBaseBranch = parentEpic.id
								epicWorktreePath = epicWorktree.path
								yield* Effect.log(`Child task ${issueId} will branch from epic ${parentEpic.id}`)
							}
						}

						// Create worktree (idempotent - returns existing if present)
						// copyPaths are applied to ALL worktrees:
						// - Epic children: copy from epic's worktree (epicWorktreePath)
						// - Regular tasks: copy from main project (projectPath fallback)
						const worktree = yield* worktreeManager.create({
							issueId: issueId,
							projectPath,
							baseBranch: effectiveBaseBranch,
							sourceWorktreePath: epicWorktreePath,
							copyPaths: worktreeConfig.copyPaths,
							preCompactEnabled: hooksConfig.preCompact.enabled,
						})

						// NOTE: .claude/ directory is git-tracked so it's already in the worktree.
						// WorktreeManager.copyClaudeLocalSettings handles settings.local.json (gitignored).
						// No additional copying needed here.

						// Get session, CLI tool, and model config from current project
						// Note: worktreeConfig was already fetched above for copyPaths
						const sessionConfig = yield* appConfig.getSessionConfig()
						const cliTool = yield* appConfig.getCliTool()
						const modelConfig = yield* appConfig.getModelConfig()

						// DEBUG: Log which CLI tool is being used
						yield* Effect.log(`[DEBUG] cliTool from config: ${cliTool}`)

						// Get the tool definition for command building
						const toolDef = getToolDefinition(cliTool)

						// Generate tmux session name (just the issueId)
						const tmuxSessionName = getIssueSessionName(issueId)

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
						const toolModelConfig = modelConfig[cliTool]
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
								let updatedIssueStatus = false

								if (!hasSession) {
									yield* worktreeSession.getOrCreateSession(issueId, {
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
									yield* issueTrackerClient.update(issueId, { status: "in_progress" })
									updatedIssueStatus = true
								}

								return { createdNewSession, updatedIssueStatus }
							}),

							// USE: Register session in memory and publish event
							() =>
								Effect.gen(function* () {
									// Session starts as "initializing" - init commands and Claude are now chained
									// in the tmux session, so if init fails, Claude won't start
									const initialState: SessionState = "initializing"

									// Create session object
									const sessionObj: Session = {
										issueId,
										worktreePath: worktree.path,
										tmuxSessionName,
										state: initialState,
										startedAt: yield* DateTime.now,
										projectPath,
									}

									// Store session in registry
									yield* Ref.update(sessionsRef, (sessions) =>
										HashMap.set(sessions, issueId, sessionObj),
									)

									// Persist to disk
									const sessions = yield* Ref.get(sessionsRef)
									yield* persistSessions(sessions)

									// Publish state change event (from idle to initial state)
									yield* publishStateChange(issueId, "idle", initialState)

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
											if (acquired.updatedIssueStatus) {
												yield* issueTrackerClient.update(issueId, { status: "open" }).pipe(
													Effect.tap(() =>
														Effect.logWarning(`Rolled back bead ${issueId} status to open`),
													),
													Effect.catchAll(() => Effect.void),
												)
											}
										})
									: Effect.void,
						)

						return session
					}),

				stop: (issueId: string) =>
					Effect.gen(function* () {
						const sessions = yield* Ref.get(sessionsRef)
						const sessionOpt = HashMap.get(sessions, issueId)

						if (sessionOpt._tag === "None") {
							return yield* Effect.fail(
								new SessionError({
									message: "Session not found",
									issueId,
								}),
							)
						}

						const session = sessionOpt.value

						yield* Effect.log(`Stopping session for ${issueId}`)

						// Sync beads changes from worktree before killing session
						// This ensures any bd update/close commands run in the worktree get synced back to main
						yield* issueTrackerClient.sync(session.worktreePath).pipe(
							Effect.tap(() => Effect.log(`Synced beads from worktree for ${issueId}`)),
							Effect.catchAll((error) =>
								Effect.logWarning(`Sync failed for ${issueId}: ${error}`).pipe(Effect.asVoid),
							),
						)

						// Kill tmux session (ignore error if already dead)
						yield* tmuxService
							.killSession(session.tmuxSessionName)
							.pipe(Effect.catchAll(() => Effect.void))

						// Get old state for event
						const oldState = session.state

						// Remove from registry
						yield* Ref.update(sessionsRef, (sessions) => HashMap.remove(sessions, issueId))

						// Persist to disk
						const updatedSessions = yield* Ref.get(sessionsRef)
						yield* persistSessions(updatedSessions)

						// Publish state change event
						yield* publishStateChange(issueId, oldState, "idle")

						yield* Effect.log(`Session stopped for ${issueId} (was: ${oldState})`)
					}),

				pause: (issueId: string) =>
					Effect.gen(function* () {
						const sessions = yield* Ref.get(sessionsRef)
						const sessionOpt = HashMap.get(sessions, issueId)

						if (sessionOpt._tag === "None") {
							return yield* Effect.fail(
								new SessionError({
									message: "Session not found",
									issueId,
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
						yield* issueTrackerClient.sync(session.worktreePath).pipe(
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
							HashMap.set(sessions, issueId, updatedSession),
						)

						// Persist to disk
						const allSessions = yield* Ref.get(sessionsRef)
						yield* persistSessions(allSessions)

						// Publish state change
						yield* publishStateChange(issueId, oldState, "paused")
					}),

				resume: (issueId: string) =>
					Effect.gen(function* () {
						const sessions = yield* Ref.get(sessionsRef)
						const sessionOpt = HashMap.get(sessions, issueId)

						if (sessionOpt._tag === "None") {
							return yield* Effect.fail(
								new SessionError({
									message: "Session not found",
									issueId,
								}),
							)
						}

						const session = sessionOpt.value

						// Verify session is paused
						if (session.state !== "paused") {
							return yield* Effect.fail(
								new InvalidStateError({
									issueId,
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
							HashMap.set(sessions, issueId, updatedSession),
						)

						// Persist to disk
						const allSessions = yield* Ref.get(sessionsRef)
						yield* persistSessions(allSessions)

						// Publish state change
						yield* publishStateChange(issueId, "paused", "busy")
					}),

				recoverSession: (issueId: string) =>
					Effect.gen(function* () {
						const sessions = yield* Ref.get(sessionsRef)
						const sessionOpt = HashMap.get(sessions, issueId)

						if (sessionOpt._tag === "None") {
							return yield* Effect.fail(new SessionNotFoundError({ issueId }))
						}

						const session = sessionOpt.value

						// Verify session is crashed
						if (session.state !== "crashed") {
							return yield* Effect.fail(
								new InvalidStateError({
									issueId,
									currentState: session.state,
									expectedState: "crashed",
									operation: "recoverSession",
								}),
							)
						}

						yield* Effect.log(`Recovering crashed session for ${issueId}`)

						// Verify worktree still exists
						const worktreeExists = yield* worktreeManager
							.exists({ issueId: issueId, projectPath: session.projectPath })
							.pipe(Effect.catchAll(() => Effect.succeed(false)))

						if (!worktreeExists) {
							return yield* Effect.fail(
								new SessionError({
									message: `Worktree no longer exists at ${session.worktreePath}. Cannot recover session.`,
									issueId,
								}),
							)
						}

						// Get config for session setup
						const sessionConfig = yield* appConfig.getSessionConfig()
						const worktreeConfig = yield* appConfig.getWorktreeConfig()
						const cliTool = yield* appConfig.getCliTool()
						const modelConfig = yield* appConfig.getModelConfig()

						// Check session limit
						const maxSessions = (sessionConfig as any).maxSessions ?? 10
						const activeSessions = HashMap.reduce(sessions, 0, (count, session) =>
							session.state !== "idle" && session.state !== "crashed" ? count + 1 : count,
						)

						if (activeSessions >= maxSessions) {
							return yield* Effect.fail(
								new SessionLimitError({
									message: `Cannot recover session: Maximum session limit (${maxSessions}) reached.`,
									limit: maxSessions,
									current: activeSessions,
								}),
							)
						}

						// Get tool definition and model
						const toolDef = getToolDefinition(cliTool)
						const toolModelConfig = modelConfig[cliTool]
						const effectiveModel = toolModelConfig.default ?? modelConfig.default

						// Build command with -c flag to continue conversation
						const commandWithOptions = toolDef.buildCommand({
							continueConversation: true, // Key difference from start() - continue where we left off
							model: effectiveModel,
							dangerouslySkipPermissions: sessionConfig.dangerouslySkipPermissions,
						})

						const tmuxSessionName = getIssueSessionName(issueId)
						const { tmuxPrefix, backgroundTasks } = sessionConfig

						// Get initCommands: merge worktree config + tool-specific init commands
						const toolInitCommands = toolDef.getInitCommands()
						const initCommands = [...worktreeConfig.initCommands, ...toolInitCommands]

						// Create new tmux session in the existing worktree
						yield* worktreeSession.getOrCreateSession(issueId, {
							worktreePath: session.worktreePath,
							projectPath: session.projectPath,
							initCommands,
							tmuxPrefix,
							backgroundTasks,
						})

						// Create the code window with resumed Claude
						yield* worktreeSession.ensureWindow(tmuxSessionName, WINDOW_NAMES.CODE, {
							command: commandWithOptions,
							cwd: session.worktreePath,
						})

						// Update session state from crashed to initializing
						const recoveredSession: Session = {
							...session,
							state: "initializing",
							startedAt: yield* DateTime.now, // Reset start time for recovered session
						}

						yield* Ref.update(sessionsRef, (sessions) =>
							HashMap.set(sessions, issueId, recoveredSession),
						)

						// Persist to disk
						const allSessions = yield* Ref.get(sessionsRef)
						yield* persistSessions(allSessions)

						// Publish state change
						yield* publishStateChange(issueId, "crashed", "initializing")

						yield* Effect.log(`Successfully recovered session for ${issueId}`)

						return recoveredSession
					}),

				getState: (issueId: string) =>
					Effect.gen(function* () {
						const sessions = yield* Ref.get(sessionsRef)
						const sessionOpt = HashMap.get(sessions, issueId)

						if (sessionOpt._tag === "None") {
							return yield* Effect.fail(new SessionNotFoundError({ issueId }))
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
						const worktreeByIssueLookup = new Map(
							worktrees.map((wt) => [normalizeIssueIdForLookup(wt.issueId), wt] as const),
						)
						const persistedByIssueLookup = new Map(
							Array.from(HashMap.entries(persistedSessions), ([storedIssueId, persisted]) => [
								normalizeIssueIdForLookup(storedIssueId),
								persisted,
							]),
						)
						const inMemoryIssueLookup = new Set(
							Array.from(HashMap.keys(inMemorySessions), normalizeIssueIdForLookup),
						)

						// Build set of running tmux session names for crash detection
						const runningTmuxNames = new Set(tmuxSessions.map((s) => s.name))

						for (const tmuxSession of tmuxSessions) {
							const parsed = parseIssueSessionName(tmuxSession.name)
							if (!parsed || parsed.type !== "issue") continue

							const issueId = parsed.issueId
							const issueLookupKey = normalizeIssueIdForLookup(issueId)

							if (inMemoryIssueLookup.has(issueLookupKey)) continue

							{
								const worktree = worktreeByIssueLookup.get(issueLookupKey)
								const persisted = persistedByIssueLookup.get(issueLookupKey)

								const orphanedSession: Session = {
									issueId,
									worktreePath:
										worktree?.path ??
										persisted?.worktreePath ??
										getWorktreePath(effectiveProjectPath, issueId),
									tmuxSessionName: tmuxSession.name,
									state: persisted?.state ?? "busy",
									startedAt: persisted?.startedAt ?? DateTime.unsafeFromDate(tmuxSession.created),
									projectPath: persisted?.projectPath ?? effectiveProjectPath,
								}
								yield* Ref.update(sessionsRef, (sessions) =>
									HashMap.set(sessions, issueId, orphanedSession),
								)
							}
						}

						// Detect crashed sessions: persisted sessions whose tmux died
						// States that indicate an active tmux session should exist:
						const activeStates: Set<SessionState> = new Set([
							"initializing",
							"busy",
							"waiting",
							"paused",
							"warning",
						])

						for (const [issueId, persisted] of HashMap.entries(persistedSessions)) {
							const issueLookupKey = normalizeIssueIdForLookup(issueId)
							// Skip if already recovered from tmux (handled above)
							if (inMemoryIssueLookup.has(issueLookupKey)) continue

							// Check if this session was active but tmux died
							if (
								activeStates.has(persisted.state) &&
								!runningTmuxNames.has(persisted.tmuxSessionName)
							) {
								yield* Effect.log(
									`Detected crashed session for ${issueId} (was ${persisted.state}, tmux gone)`,
								)

								const crashedSession: Session = {
									issueId,
									worktreePath: persisted.worktreePath,
									tmuxSessionName: persisted.tmuxSessionName,
									state: "crashed",
									startedAt: persisted.startedAt,
									projectPath: persisted.projectPath,
								}
								yield* Ref.update(sessionsRef, (sessions) =>
									HashMap.set(sessions, issueId, crashedSession),
								)
							}
						}

						// Return updated list
						const updatedSessions = yield* Ref.get(sessionsRef)
						return Array.from(HashMap.values(updatedSessions))
					}),

				updateState: (issueId: string, newState: SessionState) =>
					Effect.gen(function* () {
						const sessions = yield* Ref.get(sessionsRef)
						const sessionOpt = HashMap.get(sessions, issueId)

						if (sessionOpt._tag === "None") {
							return yield* Effect.fail(new SessionNotFoundError({ issueId }))
						}

						const session = sessionOpt.value
						const oldState = session.state

						const updatedSession: Session = {
							...session,
							state: newState,
						}

						yield* Ref.update(sessionsRef, (sessions) =>
							HashMap.set(sessions, issueId, updatedSession),
						)

						// Persist to disk
						const allSessions = yield* Ref.get(sessionsRef)
						yield* persistSessions(allSessions)

						// Publish state change
						yield* publishStateChange(issueId, oldState, newState)
					}),

				updateStateFromTmux: (
					issueId: string,
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
						const sessionOpt = HashMap.get(sessions, issueId)

						// If session doesn't exist but we have metadata, create it (orphan recovery)
						if (sessionOpt._tag === "None") {
							if (sessionMeta) {
								yield* Effect.log(`Recovering orphaned session for ${issueId} (status: ${status})`)

								// Map status to SessionState
								let initialState: SessionState = "busy"
								if (status === "waiting") initialState = "waiting"
								if (status === "idle") initialState = "idle"

								const orphanedSession: Session = {
									issueId,
									worktreePath:
										sessionMeta.worktreePath ??
										getWorktreePath(sessionMeta.projectPath ?? process.cwd(), issueId),
									tmuxSessionName: sessionMeta.sessionName,
									state: initialState,
									startedAt: DateTime.unsafeFromDate(new Date(sessionMeta.createdAt * 1000)),
									projectPath: sessionMeta.projectPath ?? process.cwd(),
								}

								yield* Ref.update(sessionsRef, (sessions) =>
									HashMap.set(sessions, issueId, orphanedSession),
								)

								// Persist the recovered session
								const allSessions = yield* Ref.get(sessionsRef)
								yield* persistSessions(allSessions)

								// Publish as a new session discovery (idle -> currentState)
								yield* publishStateChange(issueId, "idle", initialState)
								return
							}
							return yield* Effect.fail(new SessionNotFoundError({ issueId }))
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
							HashMap.set(sessions, issueId, updatedSession),
						)

						// Persist to disk
						const allSessions = yield* Ref.get(sessionsRef)
						yield* persistSessions(allSessions)

						// Publish state change
						yield* publishStateChange(issueId, oldState, newState)
					}),

				subscribeToStateChanges: () => Effect.succeed(stateChangeHub),
			}
		}),
	},
) {}
