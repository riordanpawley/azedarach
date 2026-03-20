/**
 * Session Management Atoms
 *
 * Handles Claude session lifecycle: start, stop, pause, resume.
 * Also includes tmux session monitoring and PTY metrics.
 */

import { DaemonRpcClient } from "@azedarach/shared/rpc"
import { Effect } from "effect"
import { getTuiProjectContextRead } from "../services/TuiProjectContextService.js"
import { AttachmentService, TmuxSessionMonitor } from "../utils/runtimeServices.js"
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

type TmuxSessionMonitorUpdate = {
	readonly issueId: string
	readonly status: "busy" | "waiting" | "idle"
	readonly sessionName: string
	readonly createdAt: number
	readonly worktreePath: string | null
	readonly projectPath: string | null
}

const toStartedAtIso = (createdAt: number): string | null =>
	createdAt > 0 ? new Date(createdAt * 1000).toISOString() : null

export const buildSessionUpdateStateRequest = (
	update: TmuxSessionMonitorUpdate,
	currentProjectPath: string | null | undefined,
): {
	readonly issueId: string
	readonly state: "busy" | "waiting" | "idle"
	readonly projectPath: string
	readonly tmuxSessionName: string
	readonly worktreePath: string | null
	readonly startedAt: string | null
} => ({
	issueId: update.issueId,
	state: update.status,
	projectPath: update.projectPath ?? currentProjectPath ?? process.cwd(),
	tmuxSessionName: update.sessionName,
	worktreePath: update.worktreePath,
	startedAt: toStartedAtIso(update.createdAt),
})

export const sessionMonitorStarterAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const monitor = yield* TmuxSessionMonitor
		const projectContext = yield* getTuiProjectContextRead
		const daemonRpcClient = yield* Effect.serviceOption(DaemonRpcClient)
		const currentProjectPath = yield* projectContext.getCurrentPath()
		const sessionUpdateState =
			daemonRpcClient._tag === "Some" ? daemonRpcClient.value.sessionUpdateState : undefined

		yield* monitor.start((update) =>
			Effect.gen(function* () {
				if (sessionUpdateState === undefined) {
					return
				}

				yield* sessionUpdateState(buildSessionUpdateStateRequest(update, currentProjectPath))
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
// Session Action Atoms
// ============================================================================

/**
 * Start a Claude session (creates worktree + tmux + launches Claude)
 *
 * Usage: const startSession = useAtomSet(startSessionAtom, { mode: "promise" })
 *        await startSession(issueId)
 */
export const startSessionAtom = appRuntime.fn((issueId: string) =>
	Effect.gen(function* () {
		const projectContext = yield* getTuiProjectContextRead
		const daemonRpcClient = yield* Effect.serviceOption(DaemonRpcClient)

		// Get current project path (or cwd if no project selected)
		const projectPath = (yield* projectContext.getCurrentPath()) ?? process.cwd()
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
		return session
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Pause a running session (Ctrl+C + WIP commit)
 */
export const pauseSessionAtom = appRuntime.fn((issueId: string) =>
	Effect.gen(function* () {
		const projectContext = yield* getTuiProjectContextRead
		const daemonRpcClient = yield* Effect.serviceOption(DaemonRpcClient)
		const projectPath = (yield* projectContext.getCurrentPath()) ?? process.cwd()
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
		const projectContext = yield* getTuiProjectContextRead
		const daemonRpcClient = yield* Effect.serviceOption(DaemonRpcClient)
		const projectPath = (yield* projectContext.getCurrentPath()) ?? process.cwd()
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
 */
export const stopSessionAtom = appRuntime.fn((issueId: string) =>
	Effect.gen(function* () {
		const projectContext = yield* getTuiProjectContextRead
		const daemonRpcClient = yield* Effect.serviceOption(DaemonRpcClient)
		const projectPath = (yield* projectContext.getCurrentPath()) ?? process.cwd()
		const sessionRpc = daemonRpcClient._tag === "Some" ? daemonRpcClient.value : undefined

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
