/**
 * WorktreeSessionService - Generic tmux session management for worktrees
 *
 * Handles the common pattern of:
 * 1. Creating a tmux session with an interactive shell
 * 2. Running initCommands (e.g., "direnv allow", "envdev", "pnpm install")
 * 3. Running a main command (e.g., "claude", "pnpm run dev")
 *
 * CRITICAL: Init commands and main command run in the SAME tmux session shell.
 * This ensures environment set by direnv/envdev persists for the main command.
 *
 * This service is agnostic about WHAT runs in the session - it could be:
 * - Claude Code (via ClaudeSessionManager)
 * - A dev server (via DevServerService)
 * - Any other long-running process
 *
 * Key features:
 * - Chains initCommands with main command using && (fail-fast)
 * - Uses interactive shell (-i) for alias/function support
 * - Environment persists from init commands to main command
 * - Keeps session alive after command exits (exec $shell fallback)
 * - Configurable shell and tmux prefix from AppConfig
 */

import type { CommandExecutor } from "@effect/platform"
import { Data, Effect } from "effect"
import { AppConfig, type ResolvedConfig } from "../config/index.js"
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
					WorktreeSessionError | TmuxError,
					CommandExecutor.CommandExecutor
				> =>
					Effect.gen(function* () {
						const {
							sessionName,
							worktreePath,
							command,
							cwd = worktreePath,
							tmuxPrefix = config.session.tmuxPrefix,
							initCommands = [],
						} = options

						// Get shell from config (for interactive mode)
						const shell = config.session.shell

						// Build the full command chain:
						// 1. Init commands (chained with &&, so failure stops the chain)
						// 2. Main command
						// 3. Keep shell alive for debugging (exec $shell)
						//
						// Example: zsh -i -c 'direnv allow && envdev && pnpm install && claude "prompt"; exec zsh'
						//
						// CRITICAL: Everything runs in the SAME shell, so:
						// - Aliases like "envdev" work (interactive shell loads .zshrc)
						// - Environment from direnv/envdev persists for main command
						// - Any failure in init commands prevents main command from running

						let commandChain: string
						if (initCommands.length > 0) {
							// Chain init commands with && (fail-fast), then run main command
							const initChain = initCommands.join(" && ")
							commandChain = `${initChain} && ${command}`
						} else {
							// No init commands, just run main command
							commandChain = command
						}

						// Wrap in interactive shell with exec fallback
						const fullCommand = `${shell} -i -c '${commandChain}; exec ${shell}'`

						yield* Effect.log(`Creating tmux session: ${sessionName}`)
						yield* Effect.log(`Command chain: ${commandChain}`)

						// Create tmux session
						yield* tmux.newSession(sessionName, {
							cwd,
							command: fullCommand,
							prefix: tmuxPrefix,
						})

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
