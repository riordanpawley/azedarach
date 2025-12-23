/**
 * WorktreeSessionService - Generic tmux session management for worktrees
 *
 * Handles the common pattern of:
 * 1. Creating a tmux session with an interactive shell
 * 2. Running initCommands (e.g., "direnv allow", "envdev", "pnpm install")
 * 3. Running a main command (e.g., "claude", "pnpm run dev")
 *
 * CRITICAL: Init commands run SEQUENTIALLY via send-keys, NOT chained with &&.
 * This allows the shell prompt to appear between commands, which:
 * - Triggers direnv hooks to load environment after "direnv allow"
 * - Enables proper failure detection for each command
 * - Ensures environment persists correctly for subsequent commands
 *
 * This service is agnostic about WHAT runs in the session - it could be:
 * - Claude Code (via ClaudeSessionManager)
 * - A dev server (via DevServerService)
 * - Any other long-running process
 *
 * Key features:
 * - Sends initCommands one at a time, waiting for each to complete
 * - Uses interactive shell (-i) for alias/function support and direnv hooks
 * - Monitors for command failures via exit status marker
 * - Keeps session alive after command exits (exec $shell fallback)
 * - Configurable shell and tmux prefix from AppConfig
 */

import type { CommandExecutor } from "@effect/platform"
import { Data, Effect, Option, Schedule } from "effect"
import { AppConfig } from "../config/index.js"
import { SessionNotFoundError, TmuxError, TmuxService } from "./TmuxService.js"

// ============================================================================
// Types
// ============================================================================

/**
 * Options for creating a worktree session
 */
export interface CreateWorktreeSessionOptions {
	/** Unique session name for tmux */
	readonly sessionName: string
	/** Path to the worktree directory */
	readonly worktreePath: string
	/** Path to the main project directory (for crash recovery via tmux state) */
	readonly projectPath?: string
	/** Command to run in the session (e.g., "claude", "pnpm run dev") */
	readonly command: string
	/** Working directory for the command (defaults to worktreePath) */
	readonly cwd?: string
	/** Custom tmux prefix key (overrides config) */
	readonly tmuxPrefix?: string
	/**
	 * Init commands to run before main command (chained with &&).
	 * IMPORTANT: Caller should load these from the TARGET project's config,
	 * not from the azedarach app's config.
	 * If undefined, no init commands are run.
	 */
	readonly initCommands?: readonly string[]
	/**
	 * Background tasks to run in separate tmux windows.
	 * These run initCommands followed by the task command.
	 */
	readonly backgroundTasks?: readonly string[]
}

/**
 * Result of creating a worktree session
 */
export interface WorktreeSessionResult {
	/** The tmux session name */
	readonly sessionName: string
	/** Path to the worktree */
	readonly worktreePath: string
}

// ============================================================================
// Error Types
// ============================================================================

/**
 * Error when session creation fails
 */
export class WorktreeSessionError extends Data.TaggedError("WorktreeSessionError")<{
	readonly message: string
	readonly sessionName: string
	readonly worktreePath?: string
}> {}

/**
 * Error when shell initialization times out
 */
export class ShellNotReadyError extends Data.TaggedError("ShellNotReadyError")<{
	readonly message: string
	readonly target: string
	readonly markerKey: string
}> {}

// ============================================================================
// Service Implementation
// ============================================================================

export class WorktreeSessionService extends Effect.Service<WorktreeSessionService>()(
	"WorktreeSessionService",
	{
		dependencies: [TmuxService.Default, AppConfig.Default],
		effect: Effect.gen(function* () {
			const tmux = yield* TmuxService
			const appConfig = yield* AppConfig

			const waitForTmuxOption = (sessionName: string, optionKey: string, errorMessage: string) =>
				Effect.retry(
					Effect.gen(function* () {
						const option = yield* tmux.getUserOption(sessionName, optionKey)
						if (Option.isNone(option) || Option.getOrThrow(option) !== "1") {
							return yield* Effect.fail(
								new ShellNotReadyError({
									message: errorMessage,
									target: sessionName,
									markerKey: optionKey,
								}),
							)
						}
					}),
					{
						times: 300,
						schedule: Schedule.spaced("200 millis"),
					},
				)

			const waitForShellReady = (target: string, markerKey: string) =>
				Effect.gen(function* () {
					// Give shell time to initialize before sending marker
					yield* Effect.sleep("500 millis")

					const readyMarker = `tmux set-option -t ${target.split(":")[0]} ${markerKey} 1`
					yield* tmux.sendKeys(target, readyMarker)

					yield* waitForTmuxOption(
						target.split(":")[0],
						markerKey,
						`Shell not ready for ${target} (marker: ${markerKey})`,
					)
				})

			return {
				getOrCreateSession: (
					beadId: string,
					options: {
						worktreePath: string
						projectPath?: string
						initCommands?: readonly string[]
						tmuxPrefix?: string
						backgroundTasks?: readonly string[]
					},
				) =>
					Effect.gen(function* () {
						const sessionName = beadId
						const exists = yield* tmux.hasSession(sessionName)
						const sessionConfig = yield* appConfig.getSessionConfig()
						const shell = sessionConfig.shell

						if (!exists) {
							yield* Effect.log(`Creating tmux session for bead: ${sessionName}`)
							yield* tmux.newSession(sessionName, {
								cwd: options.worktreePath,
								command: `${shell} -i`,
								prefix: options.tmuxPrefix ?? sessionConfig.tmuxPrefix,
								azOptions: {
									worktreePath: options.worktreePath,
									projectPath: options.projectPath,
								},
							})

							yield* waitForShellReady(sessionName, "@az_shell_ready")

							if (options.initCommands && options.initCommands.length > 0) {
								for (const cmd of options.initCommands) {
									yield* tmux.sendKeys(sessionName, cmd)
								}
							}

							const marker = `tmux set-option -t ${sessionName} @az_init_done 1`
							yield* tmux.sendKeys(sessionName, marker)

							// Wait for init commands to complete before allowing window creation
							// Wait up to 60 seconds (300 * 200ms)
							yield* waitForTmuxOption(
								sessionName,
								"@az_init_done",
								`Init commands not complete for session ${sessionName}`,
							)

							// Spawn background tasks in separate windows after init completes
							const backgroundTasks = options.backgroundTasks ?? []
							for (let i = 0; i < backgroundTasks.length; i++) {
								const task = backgroundTasks[i]
								const windowName = `task-${i + 1}`
								yield* Effect.log(`Spawning background task window: ${windowName} (${task})`)

								// Create a new window for the background task
								yield* tmux.newWindow(sessionName, windowName, {
									cwd: options.worktreePath,
									command: `${shell} -i`,
								})

								const target = `${sessionName}:${windowName}`
								yield* waitForShellReady(target, `@az_task_ready_${i + 1}`)

								// Run initCommands in the background window (environment setup)
								if (options.initCommands && options.initCommands.length > 0) {
									for (const initCmd of options.initCommands) {
										yield* tmux.sendKeys(target, initCmd)
									}
								}

								// Run the background task command followed by exec $SHELL to keep it open
								yield* tmux.sendKeys(target, `${task}; exec ${shell}`)
							}
						}

						return sessionName
					}),

				ensureWindow: (
					sessionName: string,
					windowName: string,
					options: {
						command: string
						cwd?: string
						initCommands?: readonly string[]
					},
				) =>
					Effect.gen(function* () {
						const sessionConfig = yield* appConfig.getSessionConfig()
						const shell = sessionConfig.shell
						const target = `${sessionName}:${windowName}`

						const windowExists = yield* tmux.hasWindow(sessionName, windowName)

						if (!windowExists) {
							yield* tmux.newWindow(sessionName, windowName, {
								cwd: options.cwd,
								command: `${shell} -i`,
							})

							yield* waitForShellReady(target, `@az_window_ready_${windowName}`)

							const waitCmd = `until [ "$(tmux show-option -t ${sessionName} -v @az_init_done 2>/dev/null)" = "1" ]; do sleep 1; done`
							yield* tmux.sendKeys(target, waitCmd)

							yield* Effect.log(
								`[ensureWindow] Shell ready for ${target}, waiting for init to finish`,
							)

							if (options.initCommands && options.initCommands.length > 0) {
								for (const cmd of options.initCommands) {
									yield* tmux.sendKeys(target, cmd)
								}
							}

							yield* tmux.sendKeys(target, options.command)
						} else {
							// Session recovered but tool isn't running - send command to existing window
							yield* Effect.log(`[ensureWindow] Window ${target} exists, sending command`)
							yield* tmux.selectWindow(sessionName, windowName)
							yield* tmux.sendKeys(target, options.command)
						}

						return target
					}),

				create: (
					options: CreateWorktreeSessionOptions,
				): Effect.Effect<
					WorktreeSessionResult,
					WorktreeSessionError | TmuxError | SessionNotFoundError,
					CommandExecutor.CommandExecutor
				> =>
					Effect.gen(function* () {
						// Get session config for shell and tmuxPrefix defaults
						const sessionConfig = yield* appConfig.getSessionConfig()

						const {
							sessionName,
							worktreePath,
							projectPath,
							command,
							cwd = worktreePath,
							tmuxPrefix = sessionConfig.tmuxPrefix,
							initCommands = [],
							backgroundTasks = [],
						} = options

						// Get shell from config (for interactive mode)
						const shell = sessionConfig.shell

						yield* Effect.log(`Creating tmux session: ${sessionName}`)

						// Create tmux session with an interactive shell
						// The -i flag loads .zshrc/.bashrc which sets up direnv hooks
						yield* tmux.newSession(sessionName, {
							cwd,
							command: `${shell} -i`,
							prefix: tmuxPrefix,
							// Store worktree and project paths in tmux session options
							// Enables crash recovery - TmuxSessionMonitor can reconstruct state from tmux
							azOptions: {
								worktreePath,
								projectPath,
							},
						})

						// Give shell time to initialize
						yield* Effect.sleep("300 millis")

						// Send each init command via send-keys
						// Zsh queues them and executes in order, with prompt appearing
						// after each - this triggers direnv hooks between commands
						for (const initCmd of initCommands) {
							yield* Effect.log(`Queuing init command: ${sessionName}:${initCmd}`)
							yield* tmux.sendKeys(sessionName, initCmd)
						}

						// Signal init completion
						// We send this to the shell so it runs AFTER initCommands complete
						const marker = `tmux set-option -t ${sessionName} @az_init_done 1`
						yield* tmux.sendKeys(sessionName, marker)

						// Send the main command last
						yield* Effect.log(`Queuing main command: ${sessionName}:${command}`)
						yield* tmux.sendKeys(sessionName, command)

						// Spawn background tasks in separate windows
						for (let i = 0; i < backgroundTasks.length; i++) {
							const task = backgroundTasks[i]
							const windowName = `task-${i + 1}`
							yield* Effect.log(`Spawning background task window: ${windowName} (${task})`)

							// Create a new window for the background task
							yield* tmux.newWindow(sessionName, windowName, {
								cwd,
								command: `${shell} -i`,
							})

							// Give shell time to initialize in the new window
							yield* Effect.sleep("300 millis")

							// Background tasks MUST wait for main session init to complete.
							// We use a shell loop to wait for the @az_init_done option to be set.
							const waitCmd = `until [ "$(tmux show-option -t ${sessionName} -v @az_init_done 2>/dev/null)" = "1" ]; do sleep 1; done`
							yield* tmux.sendKeys(`${sessionName}:${windowName}`, waitCmd)

							// Run initCommands in the background window (environment only)
							for (const initCmd of initCommands) {
								yield* tmux.sendKeys(`${sessionName}:${windowName}`, initCmd)
							}

							// Run the background task command followed by exec $SHELL to keep it open
							yield* tmux.sendKeys(`${sessionName}:${windowName}`, `${task}; exec ${shell}`)
						}

						return {
							sessionName,
							worktreePath,
						}
					}).pipe(
						Effect.mapError((e) => {
							if (
								e instanceof WorktreeSessionError ||
								e instanceof TmuxError ||
								e instanceof SessionNotFoundError
							) {
								return e
							}
							return new WorktreeSessionError({
								message: String(e),
								sessionName: options.sessionName,
								worktreePath: options.worktreePath,
							})
						}),
					),

				/**
				 * Check if a tmux session exists
				 */
				hasSession: (
					sessionName: string,
				): Effect.Effect<boolean, never, CommandExecutor.CommandExecutor> =>
					tmux.hasSession(sessionName),

				/**
				 * Kill a tmux session
				 */
				killSession: (
					sessionName: string,
				): Effect.Effect<void, SessionNotFoundError, CommandExecutor.CommandExecutor> =>
					tmux.killSession(sessionName),

				/**
				 * Send keys to a tmux session
				 */
				sendKeys: (
					sessionName: string,
					keys: string,
				): Effect.Effect<void, SessionNotFoundError, CommandExecutor.CommandExecutor> =>
					tmux.sendKeys(sessionName, keys),

				/**
				 * Capture pane output from a tmux session
				 */
				capturePane: (
					sessionName: string,
					lines?: number,
				): Effect.Effect<string, TmuxError, CommandExecutor.CommandExecutor> =>
					tmux.capturePane(sessionName, lines),

				/**
				 * Switch tmux client to a session
				 */
				switchClient: (
					sessionName: string,
				): Effect.Effect<void, SessionNotFoundError, CommandExecutor.CommandExecutor> =>
					tmux.switchClient(sessionName),

				/**
				 * List all tmux sessions
				 */
				listSessions: (): Effect.Effect<
					Array<{ name: string; created: Date; attached: boolean }>,
					TmuxError,
					CommandExecutor.CommandExecutor
				> => tmux.listSessions(),
			}
		}),
	},
) {}
