/**
 * Session Management Atoms
 *
 * Handles Claude session lifecycle: start, stop, pause, resume.
 * Also includes tmux session monitoring and PTY metrics.
 */

import { DaemonRpcClient } from "@azedarach/shared/rpc"
import { Effect } from "effect"
import { AttachmentService } from "../../../../src/core/AttachmentService.js"
import { PTYMonitor } from "../../../../src/core/PTYMonitor.js"
import { TmuxSessionMonitor } from "../../../../src/core/TmuxSessionMonitor.js"
import { ProjectService } from "../../../../src/services/ProjectService.js"
import { appRuntime } from "./runtime.js"

// ============================================================================
// TmuxSessionMonitor (Claude Code native hooks integration)
// ============================================================================

/**
 * Session monitor starter atom
 *
 * Initializes the TmuxSessionMonitor polling process.
 * This atom should be consumed at the app root to ensure state updates
 * from Claude Code hooks are processed.
 *
 * Usage: useAtomValue(sessionMonitorStarterAtom)
 */
const mapTmuxStatusToSessionState = (status: "busy" | "waiting" | "idle") => {
	if (status === "waiting") return "waiting" as const
	if (status === "idle") return "idle" as const
	return "busy" as const
}

const isSyntheticTmuxDisappearance = (update: {
	status: "busy" | "waiting" | "idle"
	createdAt: number
}) => update.status === "idle" && update.createdAt === 0

export const sessionMonitorStarterAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const monitor = yield* TmuxSessionMonitor
		const ptyMonitor = yield* PTYMonitor
		const projectService = yield* ProjectService
		const daemonRpcClient = yield* Effect.serviceOption(DaemonRpcClient)
		const currentProjectPath = yield* projectService.getCurrentPath()
		const sessionUpdateState =
			daemonRpcClient._tag === "Some" ? daemonRpcClient.value.sessionUpdateState : undefined

		yield* monitor.start((update) =>
			Effect.gen(function* () {
				// Ensure PTY monitor tracks sessions discovered via tmux (orphan recovery/startup).
				yield* ptyMonitor.registerSession(update.issueId, update.sessionName)

				if (!isSyntheticTmuxDisappearance(update)) {
					// Hook/tmux status is authoritative; stamp hook recency for PTY priority window.
					yield* ptyMonitor.recordHookSignal(
						update.issueId,
						mapTmuxStatusToSessionState(update.status),
					)
				}

				if (sessionUpdateState === undefined) {
					return
				}

				yield* sessionUpdateState({
					issueId: update.issueId,
					state: update.status,
					projectPath: update.projectPath ?? currentProjectPath ?? process.cwd(),
				})
			}).pipe(
				Effect.catchAll((error) =>
					Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
						Effect.zipRight(Effect.void),
					),
				),
			),
		)
	}).pipe(Effect.catchAll(Effect.logError)),
)

// ============================================================================
// PTY Monitor (session metrics via PTY output pattern matching)
// ============================================================================

/**
 * Session metrics atom - subscribes to PTYMonitor metrics changes
 *
 * Provides reactive access to session metrics extracted from PTY output.
 * Metrics include: estimatedTokens, agentPhase, recentOutput
 *
 * Usage: const metrics = useAtomValue(sessionMetricsAtom)
 */
export const sessionMetricsAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const ptyMonitor = yield* PTYMonitor
		return ptyMonitor.metrics
	}),
)

// ============================================================================
// Session Action Atoms
// ============================================================================

/**
 * Start a Claude session (creates worktree + tmux + launches Claude)
 *
 * Also registers the session with PTYMonitor for state detection.
 *
 * Usage: const startSession = useAtomSet(startSessionAtom, { mode: "promise" })
 *        await startSession(issueId)
 */
export const startSessionAtom = appRuntime.fn((issueId: string) =>
	Effect.gen(function* () {
		const ptyMonitor = yield* PTYMonitor
		const projectService = yield* ProjectService
		const daemonRpcClient = yield* Effect.serviceOption(DaemonRpcClient)

		// Get current project path (or cwd if no project selected)
		const projectPath = (yield* projectService.getCurrentPath()) ?? process.cwd()
		const sessionRpc = daemonRpcClient._tag === "Some" ? daemonRpcClient.value : undefined

		if (sessionRpc === undefined || sessionRpc.sessionStart === undefined) {
			yield* Effect.fail(new Error("Daemon RPC client unavailable for session start"))
			return
		}

		const session = yield* sessionRpc
			.sessionStart({
				issueId,
				projectPath,
			})
			.pipe(Effect.map((result) => result.session))

		// Register with PTYMonitor for state detection
		yield* ptyMonitor.registerSession(issueId, session.tmuxSessionName)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Pause a running session (Ctrl+C + WIP commit)
 */
export const pauseSessionAtom = appRuntime.fn((issueId: string) =>
	Effect.gen(function* () {
		const projectService = yield* ProjectService
		const daemonRpcClient = yield* Effect.serviceOption(DaemonRpcClient)
		const projectPath = (yield* projectService.getCurrentPath()) ?? process.cwd()
		const sessionRpc = daemonRpcClient._tag === "Some" ? daemonRpcClient.value : undefined

		if (sessionRpc === undefined || sessionRpc.sessionPause === undefined) {
			yield* Effect.fail(new Error("Daemon RPC client unavailable for session pause"))
			return
		}

		yield* sessionRpc.sessionPause({
			issueId,
			projectPath,
		})
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Resume a paused session
 */
export const resumeSessionAtom = appRuntime.fn((issueId: string) =>
	Effect.gen(function* () {
		const projectService = yield* ProjectService
		const daemonRpcClient = yield* Effect.serviceOption(DaemonRpcClient)
		const projectPath = (yield* projectService.getCurrentPath()) ?? process.cwd()
		const sessionRpc = daemonRpcClient._tag === "Some" ? daemonRpcClient.value : undefined

		if (sessionRpc === undefined || sessionRpc.sessionResume === undefined) {
			yield* Effect.fail(new Error("Daemon RPC client unavailable for session resume"))
			return
		}

		yield* sessionRpc.sessionResume({
			issueId,
			projectPath,
		})
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Stop a running session (kills tmux, marks as idle)
 *
 * Also unregisters the session from PTYMonitor.
 */
export const stopSessionAtom = appRuntime.fn((issueId: string) =>
	Effect.gen(function* () {
		const ptyMonitor = yield* PTYMonitor
		const projectService = yield* ProjectService
		const daemonRpcClient = yield* Effect.serviceOption(DaemonRpcClient)
		const projectPath = (yield* projectService.getCurrentPath()) ?? process.cwd()
		const sessionRpc = daemonRpcClient._tag === "Some" ? daemonRpcClient.value : undefined

		// Unregister from PTYMonitor first (before session is stopped)
		yield* ptyMonitor.unregisterSession(issueId)

		if (sessionRpc === undefined || sessionRpc.sessionStop === undefined) {
			yield* Effect.fail(new Error("Daemon RPC client unavailable for session stop"))
			return
		}

		yield* sessionRpc.sessionStop({
			issueId,
			projectPath,
		})
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Attach to a session externally (opens new terminal window)
 *
 * Usage: const attachExternal = useAtomSet(attachExternalAtom, { mode: "promise" })
 *        await attachExternal(sessionId)
 */
export const attachExternalAtom = appRuntime.fn((sessionId: string) =>
	Effect.gen(function* () {
		const service = yield* AttachmentService
		yield* service.attachExternal(sessionId)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Attach to a session inline (future: replaces TUI)
 */
export const attachInlineAtom = appRuntime.fn((sessionId: string) =>
	Effect.gen(function* () {
		const service = yield* AttachmentService
		yield* service.attachInline(sessionId)
	}).pipe(Effect.catchAll(Effect.logError)),
)
