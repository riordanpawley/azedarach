/**
 * WorktreeSessionService - Generic tmux session management for worktrees
 *
 * Handles the common pattern of:
 * 1. Running initCommands in a worktree (e.g., "direnv allow", "pnpm install")
 * 2. Creating a tmux session with an interactive shell
 * 3. Running a command in that session
 *
 * This service is agnostic about WHAT runs in the session - it could be:
 * - Claude Code (via ClaudeSessionManager)
 * - A dev server (via DevServerService)
 * - Any other long-running process
 *
 * Key features:
 * - Runs initCommands before starting the session command
 * - Uses interactive shell (-i) for direnv/environment loading
 * - Keeps session alive after command exits (exec $shell fallback)
 * - Configurable shell and tmux prefix from AppConfig
 */

import type { CommandExecutor } from "@effect/platform"
import { Data, Effect } from "effect"
import { AppConfig, type ResolvedConfig } from "../config/index.js"
import { type InitCommandError, runInitCommands } from "./initCommands.js"
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
	/** Command to run in the session (e.g., "claude", "pnpm run dev") */
	readonly command: string
	/** Working directory for the command (defaults to worktreePath) */
	readonly cwd?: string
	/** Custom tmux prefix key (overrides config) */
	readonly tmuxPrefix?: string
	/** Whether to run initCommands before starting (default: true) */
	readonly runInitCommands?: boolean
}

/**
 * Result of creating a worktree session
 */
export interface WorktreeSessionResult {
	/** The tmux session name */
	readonly sessionName: string
	/** Path to the worktree */
	readonly worktreePath: string
	/** Whether any init commands failed (only if runInitCommands was true) */
	readonly initCommandsHadFailures: boolean
	/** List of failed init commands (empty if all succeeded) */
	readonly failedInitCommands: readonly string[]
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
			const config: ResolvedConfig = appConfig.config

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
					WorktreeSessionError | TmuxError | InitCommandError,
					CommandExecutor.CommandExecutor
				> =>
					Effect.gen(function* () {
						const {
							sessionName,
							worktreePath,
							command,
							cwd = worktreePath,
							tmuxPrefix = config.session.tmuxPrefix,
							runInitCommands: shouldRunInit = true,
						} = options

						// Track init command results
						let initCommandsHadFailures = false
						let failedInitCommands: readonly string[] = []

						// Run init commands if enabled
						if (shouldRunInit) {
							const worktreeConfig = config.worktree
							const initResult = yield* runInitCommands({
								worktreePath,
								initCommands: worktreeConfig.initCommands,
								env: worktreeConfig.env,
								continueOnFailure: worktreeConfig.continueOnFailure,
								parallel: worktreeConfig.parallel,
							})

							initCommandsHadFailures = initResult.hasFailures
							failedInitCommands = initResult.failedCommands

							if (initResult.hasFailures) {
								yield* Effect.log(
									`Some init commands failed: ${initResult.failedCommands.join(", ")}`,
								)
							}
						}

						// Build the full command with interactive shell
						// Use interactive shell (-i) so .zshrc/.bashrc load, which triggers direnv hooks
						// This ensures the project's .envrc environment is loaded automatically
						// Keep session alive after command exits for debugging (exec ${shell})
						const shell = config.session.shell
						const fullCommand = `${shell} -i -c '${command}; exec ${shell}'`

						// Create tmux session
						yield* tmux.newSession(sessionName, {
							cwd,
							command: fullCommand,
							prefix: tmuxPrefix,
						})

						return {
							sessionName,
							worktreePath,
							initCommandsHadFailures,
							failedInitCommands,
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
