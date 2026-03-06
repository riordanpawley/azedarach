/**
 * SessionManager - Effect service for AI session orchestration
 *
 * Core orchestration service that manages the lifecycle of AI coding sessions:
 * - Spawns the configured AI CLI in tmux sessions
 * - Coordinates with WorktreeManager for isolated git environments
 * - Tracks session state using StateDetector for output pattern matching
 * - Publishes state change events via PubSub
 * - Maintains session registry in Ref<HashMap>
 *
 * Key features:
 * - start(issueId): Create worktree, tmux session, and launch AI CLI
 * - stop(issueId): Kill tmux session and cleanup
 * - pause(issueId): Send Ctrl+C and create WIP commit
 * - resume(issueId): Continue paused session
 * - getState(issueId): Get current session state
 * - listActive(): List all running sessions
 */

import { Command, type CommandExecutor, FileSystem } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Data, DateTime, Effect, Exit, HashMap, PubSub, Ref, Schema } from "effect"
import { AppConfig } from "../config/index.js"
import { DiagnosticsService } from "../services/DiagnosticsService.js"
import { ProjectService } from "../services/ProjectService.js"
import type { SessionState } from "../ui/types.js"
import { getToolDefinition } from "./CliToolRegistry.js"
import {
	IssueTrackerClient,
	type IssueTrackerError,
	type NotFoundError,
	type ParseError,
} from "./IssueTrackerClient.js"
import {
	getIssueSessionName,
	getWorktreePath,
	normalizeIssueIdForLookup,
	parseIssueSessionName,
	resolveIssueIdFromSessionName,
	WINDOW_NAMES,
} from "./paths.js"
import { StateDetector } from "./StateDetector.js"
import { SessionStateStore } from "./SessionStateStore.js"
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
 * Session information tracked by SessionManager
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

type TmuxAttentionStatus = "busy" | "waiting" | "idle"

interface WaitingAttentionPlan {
	readonly ringBell: boolean
	readonly nextFlag: "0" | "1"
}

const AZ_STATUS_OPTION = "@az_status"
const AZ_WAITING_ALERTED_OPTION = "@az_waiting_alerted"
const BELL_CHAR = "\u0007"
const WAITING_WINDOW_BELL_STYLE = "fg=colour226,bg=colour237,bold"
const WAITING_WINDOW_ACTIVITY_STYLE = "fg=colour220,bg=colour237,bold"

const deriveWaitingAttentionPlan = (
	status: TmuxAttentionStatus,
	currentFlagRaw: string | null,
): WaitingAttentionPlan => {
	const normalizedFlag = currentFlagRaw?.trim() === "1" ? "1" : "0"
	if (status === "waiting") {
		return {
			ringBell: normalizedFlag !== "1",
			nextFlag: "1",
		}
	}
	return {
		ringBell: false,
		nextFlag: "0",
	}
}

const mapSessionStateToTmuxAttentionStatus = (state: SessionState): TmuxAttentionStatus | null => {
	switch (state) {
		case "waiting":
			return "waiting"
		case "busy":
		case "initializing":
			return "busy"
		case "idle":
			return "idle"
		default:
			return null
	}
}

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
 * Model identifier to use for session
 *
 * Supports CLI/tool-specific model names (for example short aliases or
 * provider/model identifiers like anthropic/claude-sonnet-20241022).
 */
export type SessionModel = string

/**
 * Options for starting a session
 */
export interface StartSessionOptions {
	readonly issueId: string
	readonly projectPath: string
	readonly baseBranch?: string
	/** Optional initial prompt to send on startup (e.g., "work on bead az-123") */
	readonly initialPrompt?: string
	/** Optional model to use. Uses configured tool default if not specified. */
	readonly model?: SessionModel
	/** Run tool with --dangerously-skip-permissions (if supported) (default: false) */
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
 * SessionManager service interface
 *
 * Provides typed access to AI session orchestration with Effect error handling.
 * All operations compose WorktreeManager, TmuxService, IssueTrackerClient, and StateDetector.
 */
export interface SessionManagerService {
	/**
	 * Start a new AI session for a bead
	 *
	 * Creates a git worktree, spawns a tmux session, and launches the configured AI CLI.
	 * Idempotent: if session already exists, returns existing session.
	 *
	 * @example
	 * ```ts
	 * SessionManager.start({
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
		| IssueTrackerError
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
	 * SessionManager.stop("az-05y")
	 * ```
	 */
	readonly stop: (
		issueId: string,
	) => Effect.Effect<void, SessionError | TmuxError, CommandExecutor.CommandExecutor>

	/**
	 * Pause a running session
	 *
	 * Sends Ctrl+C to the tmux session to interrupt the CLI, then creates a WIP commit.
	 * Updates session state to "paused".
	 *
	 * @example
	 * ```ts
	 * SessionManager.pause("az-05y")
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
	 * SessionManager.resume("az-05y")
	 * ```
	 */
	readonly resume: (issueId: string) => Effect.Effect<void, SessionError | InvalidStateError, never>

	/**
	 * Recover a crashed session
	 *
	 * Respawns a session whose tmux died (e.g., computer restart).
	 * Uses tool-specific continue-conversation behavior to resume where it left off.
	 *
	 * @example
	 * ```ts
	 * SessionManager.recoverSession("az-05y")
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
	 * SessionManager.getState("az-05y")
	 * ```
	 */
	readonly getState: (issueId: string) => Effect.Effect<SessionState, SessionNotFoundError, never>

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
 * SessionManager service
 *
 * Creates a service implementation with stateful session tracking via Ref<HashMap>.
 * Composes WorktreeManager, TmuxService, IssueTrackerClient, and StateDetector services.
 *
 * @example
 * ```ts
 * const program = Effect.gen(function* () {
 *   const manager = yield* SessionManager
 *   const session = yield* manager.start({
 *     issueId: "az-123",
 *     projectPath: process.cwd()
 *   })
 *   return session
 * }).pipe(Effect.provide(SessionManager.Default))
 * ```
 */
export class SessionManager extends Effect.Service<SessionManager>()(
	"SessionManager",
	{
		dependencies: [
			WorktreeManager.Default,
			TmuxService.Default,
			IssueTrackerClient.Default,
			SessionStateStore.Default,
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
			const issueTrackerClient = yield* IssueTrackerClient
			const sessionStateStore = yield* SessionStateStore
			const appConfig = yield* AppConfig
			const projectService = yield* ProjectService
			const diagnostics = yield* DiagnosticsService

			// Note: SessionManager uses effect: not scoped:, so trackService (which uses acquireRelease)
			// would need scoped. Instead we just update health status manually.
			yield* diagnostics.updateServiceHealth({
				name: "SessionManager",
				status: "healthy",
				details: "AI session orchestration",
			})

			// Track active sessions in memory
			const sessionsRef = yield* Ref.make<HashMap.HashMap<string, Session>>(HashMap.empty())

			// PubSub for state change events
			const stateChangeHub = yield* PubSub.unbounded<SessionStateChange>()

			// ====================================================================
			// Session Persistence
			// ====================================================================

			// Layer for filesystem operations (ringing terminal bell).
			const fsLayer = BunContext.layer

			/**
			 * Get the current project path from ProjectService, falling back to process.cwd()
			 */
			const getEffectiveProjectPath = (): Effect.Effect<string> =>
				Effect.gen(function* () {
					const projectPath = yield* projectService.getCurrentPath()
					return projectPath ?? process.cwd()
				})

			// Helper: Load persisted sessions from sqlite
			const loadPersistedSessions = (projectPath?: string) =>
				Effect.gen(function* () {
					const effectiveProjectPath = projectPath ?? (yield* getEffectiveProjectPath())
					return yield* sessionStateStore.load(effectiveProjectPath)
				}).pipe(
					Effect.catchAll((error) =>
						Effect.logWarning(error).pipe(
							Effect.zipRight(Effect.succeed(HashMap.empty<string, Session>())),
						),
					),
				)

			// Helper: Save sessions to sqlite
			const persistSessions = (sessions: HashMap.HashMap<string, Session>, projectPath?: string) =>
				Effect.gen(function* () {
					const effectiveProjectPath = projectPath ?? (yield* getEffectiveProjectPath())
					yield* sessionStateStore.save(effectiveProjectPath, sessions)
				}).pipe(
					Effect.catchAll((error) => Effect.logWarning(error).pipe(Effect.zipRight(Effect.void))),
				)

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
					Effect.tapError((error) =>
						Effect.logWarning(`Recovering from error before fallback: ${String(error)}`),
					),
					Effect.orElseSucceed(() => undefined),
				)

			interface CanonicalSessionCandidate {
				readonly session: Session
				readonly isStoredCanonical: boolean
			}

			const resolveCanonicalIssueIdFromSession = (
				fallbackIssueId: string,
				sessionName: string,
				projectPath?: string | null,
			): string =>
				resolveIssueIdFromSessionName(sessionName, {
					projectPath,
					fallbackIssueId,
				}) ?? fallbackIssueId

			const canonicalizeSessionMap = (
				sessions: HashMap.HashMap<string, Session>,
				defaultProjectPath: string,
			): {
				readonly sessions: HashMap.HashMap<string, Session>
				readonly changed: boolean
			} => {
				let changed = false
				const canonicalSessions = new Map<string, CanonicalSessionCandidate>()

				for (const [storedIssueId, session] of HashMap.entries(sessions)) {
					const canonicalIssueId = resolveCanonicalIssueIdFromSession(
						storedIssueId,
						session.tmuxSessionName,
						session.projectPath || defaultProjectPath,
					)
					const canonicalSession =
						session.issueId === canonicalIssueId
							? session
							: { ...session, issueId: canonicalIssueId }

					if (storedIssueId !== canonicalIssueId || canonicalSession !== session) {
						changed = true
					}

					const candidate: CanonicalSessionCandidate = {
						session: canonicalSession,
						isStoredCanonical: storedIssueId === canonicalIssueId,
					}
					const existing = canonicalSessions.get(canonicalIssueId)
					if (!existing) {
						canonicalSessions.set(canonicalIssueId, candidate)
						continue
					}

					changed = true
					if (!existing.isStoredCanonical && candidate.isStoredCanonical) {
						canonicalSessions.set(canonicalIssueId, candidate)
					}
				}

				let normalizedSessions = HashMap.empty<string, Session>()
				for (const [issueId, candidate] of canonicalSessions.entries()) {
					normalizedSessions = HashMap.set(normalizedSessions, issueId, candidate.session)
				}

				return {
					sessions: normalizedSessions,
					changed,
				}
			}

			const normalizeProjectPathForComparison = (
				projectPath: string | null | undefined,
			): string | null => {
				const trimmed = projectPath?.trim()
				if (!trimmed) {
					return null
				}
				return trimmed.replace(/\/+$/, "")
			}

			const projectPathsEqual = (
				left: string | null | undefined,
				right: string | null | undefined,
			): boolean => {
				const normalizedLeft = normalizeProjectPathForComparison(left)
				const normalizedRight = normalizeProjectPathForComparison(right)
				if (normalizedLeft === null || normalizedRight === null) {
					return false
				}
				return normalizedLeft === normalizedRight
			}

			const isSessionInProjectScope = (session: Session, projectPath: string): boolean => {
				const sessionProjectPath = normalizeProjectPathForComparison(session.projectPath)
				if (sessionProjectPath !== null && !projectPathsEqual(sessionProjectPath, projectPath)) {
					return false
				}

				const parsed = parseIssueSessionName(session.tmuxSessionName, projectPath)
				return parsed?.type === "issue"
			}

			const filterSessionsByProjectScope = (
				sessions: HashMap.HashMap<string, Session>,
				projectPath: string,
			): HashMap.HashMap<string, Session> => {
				let scopedSessions = HashMap.empty<string, Session>()
				for (const [issueId, session] of HashMap.entries(sessions)) {
					if (!isSessionInProjectScope(session, projectPath)) {
						continue
					}
					scopedSessions = HashMap.set(scopedSessions, issueId, session)
				}
				return scopedSessions
			}

			const setTmuxSessionOption = (sessionName: string, optionName: string, value: string) =>
				Command.exitCode(
					Command.make("tmux", "set-option", "-t", sessionName, optionName, value),
				).pipe(
					Effect.asVoid,
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(Effect.void),
						),
					),
				)

			const getTmuxSessionOption = (sessionName: string, optionName: string) =>
				Command.string(
					Command.make("tmux", "show-option", "-t", sessionName, "-v", optionName),
				).pipe(
					Effect.map((value) => value.trim()),
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(Effect.succeed("")),
						),
					),
				)

			const ringSessionPaneBell = (sessionName: string) =>
				Effect.gen(function* () {
					const paneTty = yield* Command.string(
						Command.make("tmux", "display-message", "-p", "-t", sessionName, "#{pane_tty}"),
					).pipe(
						Effect.map((value) => value.trim()),
						Effect.catchAll((error) =>
							Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
								Effect.zipRight(Effect.succeed("")),
							),
						),
					)
					if (paneTty.length === 0) {
						return false
					}

					const fs = yield* FileSystem.FileSystem
					return yield* fs.writeFileString(paneTty, BELL_CHAR).pipe(
						Effect.as(true),
						Effect.catchAll((error) =>
							Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
								Effect.zipRight(Effect.succeed(false)),
							),
						),
					)
				}).pipe(Effect.provide(fsLayer))

			const applyTmuxAttentionStyles = (sessionName: string) =>
				Effect.gen(function* () {
					yield* setTmuxSessionOption(sessionName, "monitor-bell", "on")
					yield* setTmuxSessionOption(
						sessionName,
						"window-status-bell-style",
						WAITING_WINDOW_BELL_STYLE,
					)
					yield* setTmuxSessionOption(
						sessionName,
						"window-status-activity-style",
						WAITING_WINDOW_ACTIVITY_STYLE,
					)
				})

			const applyTmuxWaitingAttentionSignal = (sessionName: string, status: TmuxAttentionStatus) =>
				Effect.gen(function* () {
					yield* setTmuxSessionOption(sessionName, AZ_STATUS_OPTION, status)
					yield* applyTmuxAttentionStyles(sessionName)

					const currentFlag = yield* getTmuxSessionOption(sessionName, AZ_WAITING_ALERTED_OPTION)
					const plan = deriveWaitingAttentionPlan(
						status,
						currentFlag.length > 0 ? currentFlag : null,
					)

					let nextFlag: "0" | "1" = plan.nextFlag
					if (plan.ringBell) {
						const bellSent = yield* ringSessionPaneBell(sessionName)
						if (!bellSent) {
							nextFlag = "0"
						}
					}

					yield* setTmuxSessionOption(sessionName, AZ_WAITING_ALERTED_OPTION, nextFlag)
				})

			const syncTmuxAttentionForSessionState = (session: Session, newState: SessionState) =>
				Effect.gen(function* () {
					const status = mapSessionStateToTmuxAttentionStatus(newState)
					if (status) {
						yield* applyTmuxWaitingAttentionSignal(session.tmuxSessionName, status)
						return
					}

					// Non-status terminal states (done/error/etc.) should not keep waiting debounce armed.
					yield* setTmuxSessionOption(session.tmuxSessionName, AZ_WAITING_ALERTED_OPTION, "0")
				})

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
						const maxSessions =
							(configForLimit as { readonly maxSessions?: number }).maxSessions ?? 10
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
						const gitConfig = yield* appConfig.getGitConfig()

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
									issueTitle: parentEpic.title,
									branchSlugMaxLength: gitConfig.branchSlugMaxLength,
									projectPath,
									// Epic branches from main (no baseBranch = uses current branch)
									// Epic gets copyPaths from config (copies from main project)
									copyPaths: worktreeConfig.copyPaths,
									preCompactEnabled: hooksConfig.preCompact.enabled,
								})
								// Use the epic branch as base for the child task
								effectiveBaseBranch = epicWorktree.branch
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
							issueTitle: issue.title,
							branchSlugMaxLength: gitConfig.branchSlugMaxLength,
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
						const tmuxSessionName = getIssueSessionName(issueId, projectPath)

						// Check if bead session already exists
						const hasSession = yield* tmuxService.hasSession(tmuxSessionName)

						// Build session settings object (for tools that support session settings)
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
							issueId,
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
								// Done AFTER session creation to ensure we don't leave tracker
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
									// Session starts as "initializing" - init commands and AI CLI are chained
									// in the tmux session, so if init fails, the main command won't start
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

									// Persist to sqlite
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
													Effect.catchAll((error) =>
														Effect.logWarning(
															`Recovering after caught error: ${String(error)}`,
														).pipe(Effect.zipRight(Effect.void)),
													),
												)
											}

											// Rollback bead status if we changed it
											if (acquired.updatedIssueStatus) {
												yield* issueTrackerClient.update(issueId, { status: "open" }).pipe(
													Effect.tap(() =>
														Effect.logWarning(`Rolled back bead ${issueId} status to open`),
													),
													Effect.catchAll((error) =>
														Effect.logWarning(
															`Recovering after caught error: ${String(error)}`,
														).pipe(Effect.zipRight(Effect.void)),
													),
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

						// Sync tracker changes from worktree before killing session
						// This ensures any tracker update/close commands run in the worktree get synced back to main
						yield* issueTrackerClient.sync(session.worktreePath).pipe(
							Effect.tap(() => Effect.log(`Synced tracker from worktree for ${issueId}`)),
							Effect.catchAll((error) =>
								Effect.logWarning(`Sync failed for ${issueId}: ${error}`).pipe(Effect.asVoid),
							),
						)

						// Kill tmux session (ignore error if already dead)
						yield* tmuxService
							.killSession(session.tmuxSessionName)
							.pipe(
								Effect.catchAll((error) =>
									Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
										Effect.zipRight(Effect.void),
									),
								),
							)

						// Get old state for event
						const oldState = session.state

						// Remove from registry
						yield* Ref.update(sessionsRef, (sessions) => HashMap.remove(sessions, issueId))

						// Persist to sqlite
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

						// Send Ctrl+C to interrupt the running AI CLI
						yield* tmuxService.sendKeys(session.tmuxSessionName, "C-c")

						// Wait a moment for interrupt to process
						yield* Effect.sleep("500 millis")

						// Sync tracker changes from worktree before creating WIP commit
						// This ensures any tracker update/close commands are synced before we pause
						yield* issueTrackerClient.sync(session.worktreePath).pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.void),
								),
							), // Ignore sync errors (non-critical)
						)

						// Create WIP commit in worktree
						// Git add all changes (including synced .azedarach/ directory)
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
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.succeed(0)),
								),
							),
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

						// Persist to sqlite
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

						// Persist to sqlite
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
							.pipe(
								Effect.catchAll((error) =>
									Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
										Effect.zipRight(Effect.succeed(false)),
									),
								),
							)

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
						const maxSessions =
							(sessionConfig as { readonly maxSessions?: number }).maxSessions ?? 10
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
							issueId,
							model: effectiveModel,
							dangerouslySkipPermissions: sessionConfig.dangerouslySkipPermissions,
						})

						const tmuxSessionName = getIssueSessionName(issueId, session.projectPath)
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

						// Create the code window with resumed session command
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

						// Persist to sqlite
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
						// Falls back to process.cwd() for backwards compatibility
						const effectiveProjectPath = projectPath ?? process.cwd()
						let sessionsChanged = false

						// Get and canonicalize in-memory sessions first to collapse stale alias IDs.
						const inMemorySessionsRaw = yield* Ref.get(sessionsRef)
						const canonicalInMemoryResult = canonicalizeSessionMap(
							inMemorySessionsRaw,
							effectiveProjectPath,
						)
						const inMemorySessions = canonicalInMemoryResult.sessions
						if (canonicalInMemoryResult.changed) {
							sessionsChanged = true
							yield* Ref.set(sessionsRef, inMemorySessions)
						}
						const scopedInMemorySessions = filterSessionsByProjectScope(
							inMemorySessions,
							effectiveProjectPath,
						)

						// Query tmux for actual running sessions
						const tmuxSessions = yield* tmuxService.listSessions().pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.succeed([])),
								),
							), // If tmux fails, just use in-memory
						)

						// Load and canonicalize persisted sessions for crash recovery.
						const persistedSessionsRaw = yield* loadPersistedSessions(effectiveProjectPath)
						const canonicalPersistedResult = canonicalizeSessionMap(
							persistedSessionsRaw,
							effectiveProjectPath,
						)
						const persistedSessions = canonicalPersistedResult.sessions
						const scopedPersistedSessions = filterSessionsByProjectScope(
							persistedSessions,
							effectiveProjectPath,
						)

						// Query worktrees to get accurate paths
						const worktrees = yield* worktreeManager
							.list(effectiveProjectPath)
							.pipe(
								Effect.catchAll((error) =>
									Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
										Effect.zipRight(Effect.succeed([])),
									),
								),
							)
						const worktreeByIssueLookup = new Map(
							worktrees.map((wt) => [normalizeIssueIdForLookup(wt.issueId), wt] as const),
						)
						const persistedByIssueLookup = new Map(
							Array.from(HashMap.entries(scopedPersistedSessions), ([storedIssueId, persisted]) => [
								normalizeIssueIdForLookup(storedIssueId),
								persisted,
							]),
						)
						const inMemoryIssueLookup = new Set(
							Array.from(HashMap.keys(scopedInMemorySessions), normalizeIssueIdForLookup),
						)

						// Build set of running tmux session names for crash detection
						const runningTmuxNames = new Set(tmuxSessions.map((s) => s.name))

						for (const tmuxSession of tmuxSessions) {
							const parsed = parseIssueSessionName(tmuxSession.name, effectiveProjectPath)
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
								sessionsChanged = true
								inMemoryIssueLookup.add(issueLookupKey)
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

						for (const [issueId, persisted] of HashMap.entries(scopedPersistedSessions)) {
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
								sessionsChanged = true
								inMemoryIssueLookup.add(issueLookupKey)
							}
						}

						// Return updated list
						const updatedSessions = yield* Ref.get(sessionsRef)

						if (sessionsChanged) {
							yield* persistSessions(updatedSessions, effectiveProjectPath)
						} else if (canonicalPersistedResult.changed) {
							let mergedSessions = persistedSessions
							for (const [activeIssueId, activeSession] of HashMap.entries(updatedSessions)) {
								mergedSessions = HashMap.set(mergedSessions, activeIssueId, activeSession)
							}
							yield* persistSessions(mergedSessions, effectiveProjectPath)
						}

						const scopedUpdatedSessions = filterSessionsByProjectScope(
							updatedSessions,
							effectiveProjectPath,
						)
						return Array.from(HashMap.values(scopedUpdatedSessions))
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

						// Persist to sqlite
						const allSessions = yield* Ref.get(sessionsRef)
						yield* persistSessions(allSessions)

						// Keep tmux-native waiting alerts in sync for PTY-driven tools (for example Codex).
						yield* syncTmuxAttentionForSessionState(session, newState).pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.void),
								),
							),
						)

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
						const effectiveProjectPath = yield* getEffectiveProjectPath()
						if (
							sessionMeta?.projectPath &&
							!projectPathsEqual(sessionMeta.projectPath, effectiveProjectPath)
						) {
							yield* Effect.logDebug(
								`Ignoring tmux state update for ${issueId}: project mismatch (${sessionMeta.projectPath} != ${effectiveProjectPath})`,
							)
							return
						}

						const resolvedIssueId = sessionMeta
							? resolveCanonicalIssueIdFromSession(
									issueId,
									sessionMeta.sessionName,
									sessionMeta.projectPath,
								)
							: issueId

						let sessions = yield* Ref.get(sessionsRef)
						let sessionOpt = HashMap.get(sessions, resolvedIssueId)

						// Migrate alias keys (for example `az-ak` -> `ak`) before applying updates.
						if (sessionOpt._tag === "None" && resolvedIssueId !== issueId) {
							const aliasSessionOpt = HashMap.get(sessions, issueId)
							if (aliasSessionOpt._tag === "Some") {
								const aliasSession = aliasSessionOpt.value
								const migratedAliasSession =
									aliasSession.issueId === resolvedIssueId
										? aliasSession
										: { ...aliasSession, issueId: resolvedIssueId }

								sessions = HashMap.remove(
									HashMap.set(sessions, resolvedIssueId, migratedAliasSession),
									issueId,
								)
								yield* Ref.set(sessionsRef, sessions)
								yield* persistSessions(sessions)
								sessionOpt = HashMap.get(sessions, resolvedIssueId)
							}
						}

						// If session doesn't exist but we have metadata, create it (orphan recovery)
						if (sessionOpt._tag === "None") {
							if (sessionMeta) {
								yield* Effect.log(
									`Recovering orphaned session for ${resolvedIssueId} (status: ${status})`,
								)

								// Map status to SessionState
								let initialState: SessionState = "busy"
								if (status === "waiting") initialState = "waiting"
								if (status === "idle") initialState = "idle"

								const orphanedSession: Session = {
									issueId: resolvedIssueId,
									worktreePath:
										sessionMeta.worktreePath ??
										getWorktreePath(sessionMeta.projectPath ?? process.cwd(), resolvedIssueId),
									tmuxSessionName: sessionMeta.sessionName,
									state: initialState,
									startedAt: DateTime.unsafeFromDate(new Date(sessionMeta.createdAt * 1000)),
									projectPath: sessionMeta.projectPath ?? process.cwd(),
								}

								yield* Ref.update(sessionsRef, (sessions) =>
									HashMap.set(sessions, resolvedIssueId, orphanedSession),
								)

								// Persist the recovered session
								const allSessions = yield* Ref.get(sessionsRef)
								yield* persistSessions(allSessions)

								// Publish as a new session discovery (idle -> currentState)
								yield* publishStateChange(resolvedIssueId, "idle", initialState)
								return
							}
							return yield* Effect.fail(new SessionNotFoundError({ issueId: resolvedIssueId }))
						}

						const session = sessionOpt.value
						if (
							sessionMeta &&
							status === "idle" &&
							session.tmuxSessionName !== sessionMeta.sessionName
						) {
							// Ignore stale "session ended" signals from an out-of-date alias when
							// the tracked session for this issue uses a different tmux session name.
							return
						}
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
							HashMap.set(sessions, resolvedIssueId, updatedSession),
						)

						// Persist to sqlite
						const allSessions = yield* Ref.get(sessionsRef)
						yield* persistSessions(allSessions)

						// Publish state change
						yield* publishStateChange(resolvedIssueId, oldState, newState)
					}),

				subscribeToStateChanges: () => Effect.succeed(stateChangeHub),
			}
		}),
	},
) {}
