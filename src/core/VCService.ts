/**
 * VCService - Effect service for integrating with steveyegge/vc
 *
 * Wraps the VC (VibeCoder) binary to provide AI-supervised orchestration.
 * VC adds:
 * - AI Supervisor (Sonnet 4.5) for strategy/analysis
 * - Quality Gates (tests, lint, build)
 * - Blocker-priority scheduling
 * - Conversational natural language interface
 *
 * Integration approach:
 * - Azedarach provides TUI Kanban visualization
 * - VC provides AI-supervised execution engine
 * - Both share the same Beads SQLite database
 *
 * @see https://github.com/steveyegge/vc
 */

import { Command, type CommandExecutor } from "@effect/platform"
import { Data, Effect, Ref, Schedule } from "effect"
import { type TmuxError, TmuxService } from "./TmuxService.js"

// ============================================================================
// Constants
// ============================================================================

/** tmux session name for VC REPL */
const VC_SESSION_NAME = "vc-autopilot"

/** How often to poll VC status (ms) */
const STATUS_POLL_INTERVAL = 5000

// ============================================================================
// Type Definitions
// ============================================================================

/**
 * VC executor status
 */
export type VCStatus = "not_installed" | "stopped" | "starting" | "running" | "error"

/**
 * VC executor info
 */
export interface VCExecutorInfo {
	readonly status: VCStatus
	readonly sessionName: string
	readonly pid?: number
	readonly startedAt?: Date
	readonly lastActivity?: Date
}

// ============================================================================
// Error Types
// ============================================================================

/**
 * VC not installed error
 */
export class VCNotInstalledError extends Data.TaggedError("VCNotInstalledError")<{
	readonly message: string
}> {}

/**
 * VC execution error
 */
export class VCError extends Data.TaggedError("VCError")<{
	readonly message: string
	readonly command?: string
	readonly stderr?: string
}> {}

/**
 * VC not running error
 */
export class VCNotRunningError extends Data.TaggedError("VCNotRunningError")<{
	readonly message: string
}> {}

// ============================================================================
// Service Definition
// ============================================================================

/**
 * VCService interface
 *
 * Provides integration with the VC (VibeCoder) orchestration engine.
 */
export interface VCServiceImpl {
	/**
	 * Check if VC binary is installed and available
	 *
	 * @example
	 * ```ts
	 * const available = yield* VCService.isAvailable()
	 * if (!available) {
	 *   console.log("Install VC: brew tap steveyegge/vc && brew install vc")
	 * }
	 * ```
	 */
	readonly isAvailable: () => Effect.Effect<boolean, never, CommandExecutor.CommandExecutor>

	/**
	 * Get VC version string
	 *
	 * @example
	 * ```ts
	 * const version = yield* VCService.getVersion()
	 * // "vc version 0.1.0"
	 * ```
	 */
	readonly getVersion: () => Effect.Effect<
		string,
		VCNotInstalledError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Start VC executor in auto-pilot mode
	 *
	 * Spawns VC in a tmux session where it will:
	 * - Poll for ready issues
	 * - Claim and execute work autonomously
	 * - Run quality gates
	 * - Create/update issues as needed
	 *
	 * @example
	 * ```ts
	 * yield* VCService.startAutoPilot()
	 * ```
	 */
	readonly startAutoPilot: () => Effect.Effect<
		VCExecutorInfo,
		VCNotInstalledError | VCError | TmuxError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Stop VC auto-pilot executor
	 *
	 * Gracefully stops the VC session.
	 *
	 * @example
	 * ```ts
	 * yield* VCService.stopAutoPilot()
	 * ```
	 */
	readonly stopAutoPilot: () => Effect.Effect<
		void,
		VCError | TmuxError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Check if auto-pilot is currently running
	 *
	 * @example
	 * ```ts
	 * const running = yield* VCService.isAutoPilotRunning()
	 * ```
	 */
	readonly isAutoPilotRunning: () => Effect.Effect<boolean, never, CommandExecutor.CommandExecutor>

	/**
	 * Get current executor status
	 *
	 * @example
	 * ```ts
	 * const info = yield* VCService.getStatus()
	 * console.log(`VC: ${info.status}`)
	 * ```
	 */
	readonly getStatus: () => Effect.Effect<VCExecutorInfo, never, CommandExecutor.CommandExecutor>

	/**
	 * Send a command to the VC REPL
	 *
	 * For conversational commands like:
	 * - "What's ready to work on?"
	 * - "Let's continue working"
	 * - "Add Docker support"
	 *
	 * @example
	 * ```ts
	 * yield* VCService.sendCommand("What's ready to work on?")
	 * ```
	 */
	readonly sendCommand: (
		command: string,
	) => Effect.Effect<void, VCNotRunningError | TmuxError, CommandExecutor.CommandExecutor>

	/**
	 * Attach to the VC tmux session
	 *
	 * Returns the tmux attach command for the user to run.
	 *
	 * @example
	 * ```ts
	 * const cmd = yield* VCService.getAttachCommand()
	 * // "tmux attach -t vc-autopilot"
	 * ```
	 */
	readonly getAttachCommand: () => Effect.Effect<
		string,
		VCNotRunningError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Toggle auto-pilot mode
	 *
	 * If running, stops it. If stopped, starts it.
	 *
	 * @example
	 * ```ts
	 * const newStatus = yield* VCService.toggleAutoPilot()
	 * ```
	 */
	readonly toggleAutoPilot: () => Effect.Effect<
		VCExecutorInfo,
		VCNotInstalledError | VCError | TmuxError,
		CommandExecutor.CommandExecutor
	>
}

// ============================================================================
// Implementation
// ============================================================================

/**
 * VCService
 *
 * @example
 * ```ts
 * const program = Effect.gen(function* () {
 *   const vc = yield* VCService
 *   const status = yield* vc.getStatus()
 *   return status
 * }).pipe(Effect.provide(VCService.Default))
 * ```
 */
export class VCService extends Effect.Service<VCService>()("VCService", {
	dependencies: [TmuxService.Default],
	scoped: Effect.gen(function* () {
		const tmux = yield* TmuxService

		// Track executor state
		const executorStateRef = yield* Ref.make<VCExecutorInfo>({
			status: "stopped",
			sessionName: VC_SESSION_NAME,
		})

		/**
		 * Check if vc binary exists
		 */
		const checkVCInstalled = (): Effect.Effect<boolean, never, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				const cmd = Command.make("which", "vc")
				const result = yield* Command.exitCode(cmd).pipe(
					Effect.map((code) => code === 0),
					Effect.catchAll(() => Effect.succeed(false)),
				)
				return result
			})

		/**
		 * Update executor state
		 */
		const updateState = (
			update: Partial<VCExecutorInfo>,
		): Effect.Effect<VCExecutorInfo, never, never> =>
			Ref.updateAndGet(executorStateRef, (current) => ({
				...current,
				...update,
			}))

		yield* Effect.scheduleForked(Schedule.spaced(STATUS_POLL_INTERVAL))(
			Effect.log("todo: poll vc status"),
		)

		return {
			isAvailable: () => checkVCInstalled(),

			getVersion: () =>
				Effect.gen(function* () {
					const installed = yield* checkVCInstalled()
					if (!installed) {
						return yield* Effect.fail(
							new VCNotInstalledError({
								message:
									"VC is not installed. Install with: brew tap steveyegge/vc && brew install vc",
							}),
						)
					}

					const cmd = Command.make("vc", "--version")
					const version = yield* Command.string(cmd).pipe(
						Effect.map((s) => s.trim()),
						Effect.mapError(
							(e) =>
								new VCNotInstalledError({
									message: `Failed to get VC version: ${e}`,
								}),
						),
					)

					return version
				}),

			startAutoPilot: () =>
				Effect.gen(function* () {
					// Check if VC is installed
					const installed = yield* checkVCInstalled()
					if (!installed) {
						return yield* Effect.fail(
							new VCNotInstalledError({
								message:
									"VC is not installed. Install with: brew tap steveyegge/vc && brew install vc",
							}),
						)
					}

					// Check if already running
					const hasSession = yield* tmux.hasSession(VC_SESSION_NAME)
					if (hasSession) {
						// Already running, just return current state
						return yield* updateState({
							status: "running",
							lastActivity: new Date(),
						})
					}

					// Update state to starting
					yield* updateState({ status: "starting" })

					// Start VC in a new tmux session
					// VC's REPL will handle the event loop
					yield* tmux.newSession(VC_SESSION_NAME, {
						command: "vc",
					})

					// Give it a moment to start
					yield* Effect.sleep("1 second")

					// Verify it's running
					const running = yield* tmux.hasSession(VC_SESSION_NAME)
					if (!running) {
						yield* updateState({ status: "error" })
						return yield* Effect.fail(
							new VCError({
								message: "Failed to start VC session",
							}),
						)
					}

					// Update state to running
					return yield* updateState({
						status: "running",
						startedAt: new Date(),
						lastActivity: new Date(),
					})
				}),

			stopAutoPilot: () =>
				Effect.gen(function* () {
					const hasSession = yield* tmux.hasSession(VC_SESSION_NAME)
					if (!hasSession) {
						// Already stopped
						yield* updateState({ status: "stopped" })
						return
					}

					// Send exit command gracefully first
					yield* tmux.sendKeys(VC_SESSION_NAME, "/exit").pipe(Effect.catchAll(() => Effect.void))

					// Wait a moment for graceful shutdown
					yield* Effect.sleep("500 millis")

					// Force kill if still running
					const stillRunning = yield* tmux.hasSession(VC_SESSION_NAME)
					if (stillRunning) {
						yield* tmux.killSession(VC_SESSION_NAME).pipe(Effect.catchAll(() => Effect.void))
					}

					yield* updateState({
						status: "stopped",
						startedAt: undefined,
						pid: undefined,
					})
				}),

			isAutoPilotRunning: () =>
				Effect.gen(function* () {
					const hasSession = yield* tmux
						.hasSession(VC_SESSION_NAME)
						.pipe(Effect.catchAll(() => Effect.succeed(false)))

					// Sync our state with reality
					if (hasSession) {
						yield* updateState({ status: "running", lastActivity: new Date() })
					} else {
						const current = yield* Ref.get(executorStateRef)
						if (current.status === "running") {
							yield* updateState({ status: "stopped" })
						}
					}

					return hasSession
				}),

			getStatus: () =>
				Effect.gen(function* () {
					// Check actual tmux session state
					const hasSession = yield* tmux
						.hasSession(VC_SESSION_NAME)
						.pipe(Effect.catchAll(() => Effect.succeed(false)))

					// Check if VC is installed
					const installed = yield* checkVCInstalled()

					if (!installed) {
						return yield* updateState({ status: "not_installed" })
					}

					if (hasSession) {
						return yield* updateState({ status: "running", lastActivity: new Date() })
					}

					const current = yield* Ref.get(executorStateRef)
					if (current.status === "running" || current.status === "starting") {
						// Session died unexpectedly
						return yield* updateState({ status: "stopped" })
					}

					return current
				}),

			sendCommand: (command: string) =>
				Effect.gen(function* () {
					const hasSession = yield* tmux
						.hasSession(VC_SESSION_NAME)
						.pipe(Effect.catchAll(() => Effect.succeed(false)))

					if (!hasSession) {
						return yield* Effect.fail(
							new VCNotRunningError({
								message: "VC auto-pilot is not running. Start it first with toggleAutoPilot()",
							}),
						)
					}

					// Send the command to VC's REPL via tmux
					// Map SessionNotFoundError to VCNotRunningError for consistency
					yield* tmux
						.sendKeys(VC_SESSION_NAME, command)
						.pipe(
							Effect.catchTag("SessionNotFoundError", () =>
								Effect.fail(new VCNotRunningError({ message: "VC session not found" })),
							),
						)
					yield* tmux
						.sendKeys(VC_SESSION_NAME, "Enter")
						.pipe(
							Effect.catchTag("SessionNotFoundError", () =>
								Effect.fail(new VCNotRunningError({ message: "VC session not found" })),
							),
						)

					yield* updateState({ lastActivity: new Date() })
				}),

			getAttachCommand: () =>
				Effect.gen(function* () {
					const hasSession = yield* tmux
						.hasSession(VC_SESSION_NAME)
						.pipe(Effect.catchAll(() => Effect.succeed(false)))

					if (!hasSession) {
						return yield* Effect.fail(
							new VCNotRunningError({
								message: "VC auto-pilot is not running",
							}),
						)
					}

					return `tmux attach -t ${VC_SESSION_NAME}`
				}),

			toggleAutoPilot: () =>
				Effect.gen(function* () {
					const hasSession = yield* tmux
						.hasSession(VC_SESSION_NAME)
						.pipe(Effect.catchAll(() => Effect.succeed(false)))

					if (hasSession) {
						// Stop it
						yield* tmux.killSession(VC_SESSION_NAME).pipe(Effect.catchAll(() => Effect.void))
						return yield* updateState({ status: "stopped" })
					}

					// Check if VC is installed first
					const installed = yield* checkVCInstalled()
					if (!installed) {
						return yield* Effect.fail(
							new VCNotInstalledError({
								message:
									"VC is not installed. Install with: brew tap steveyegge/vc && brew install vc",
							}),
						)
					}

					// Start it
					yield* updateState({ status: "starting" })

					yield* tmux.newSession(VC_SESSION_NAME, {
						command: "vc",
					})

					yield* Effect.sleep("1 second")

					const running = yield* tmux
						.hasSession(VC_SESSION_NAME)
						.pipe(Effect.catchAll(() => Effect.succeed(false)))

					if (running) {
						return yield* updateState({
							status: "running",
							startedAt: new Date(),
							lastActivity: new Date(),
						})
					}

					return yield* updateState({ status: "error" })
				}),
		}
	}),
}) {}

/**
 * Legacy layer export
 *
 * @deprecated Use VCService.Default instead
 */
export const VCServiceLive = VCService.Default
