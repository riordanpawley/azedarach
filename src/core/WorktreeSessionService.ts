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
import { Data, Effect } from "effect"
import { AppConfig } from "../config/index.js"
import { type SessionNotFoundError, type TmuxError, TmuxService } from "./TmuxService.js"

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

			return {
				/**
				 * Create a new tmux session in a worktree
				 *
				 * Runs initCommands first (if enabled), then creates a tmux session
				 * with an interactive shell running the specified command.
				 *
				 * @example
				 * ```ts
				 * // For Claude
				 * yield* worktreeSession.create({
				 *   sessionName: "az-123",
				 *   worktreePath: "/path/to/worktree",
				 *   command: 'claude "work on bead az-123"',
				 * })
				 *
				 * // For dev server
				 * yield* worktreeSession.create({
				 *   sessionName: "az-dev-123",
				 *   worktreePath: "/path/to/worktree",
				 *   command: "PORT=3000 pnpm run dev",
				 * })
				 * ```
				 */
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
							// Enables crash recovery - HookReceiver can reconstruct state from tmux
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
							yield* Effect.log(`Queuing init command: ${initCmd}`)
							yield* tmux.sendKeys(sessionName, initCmd)
						}

						// Send the main command last
						yield* Effect.log(`Queuing main command: ${command}`)
						yield* tmux.sendKeys(sessionName, command)

						return {
							sessionName,
							worktreePath,
						}
					}),

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
