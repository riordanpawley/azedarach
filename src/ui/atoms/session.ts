/**
 * Session Management Atoms
 *
 * Handles Claude session lifecycle: start, stop, pause, resume.
 * Also includes tmux session monitoring and PTY metrics.
 */

import { Effect } from "effect"
import { AttachmentService } from "../../core/AttachmentService.js"
import { ClaudeSessionManager } from "../../core/ClaudeSessionManager.js"
import { PTYMonitor } from "../../core/PTYMonitor.js"
import { SessionMigration } from "../../core/SessionMigration.js"
import { TmuxSessionMonitor } from "../../core/TmuxSessionMonitor.js"
import { ProjectService } from "../../services/ProjectService.js"
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
export const sessionMonitorStarterAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const monitor = yield* TmuxSessionMonitor
		const manager = yield* ClaudeSessionManager

		yield* monitor.start((update) =>
			Effect.gen(function* () {
				yield* manager.updateStateFromTmux(update.beadId, update.status)
			}).pipe(Effect.catchAll(() => Effect.void)),
		)
	}).pipe(Effect.catchAll(Effect.logError)),
)

export const sessionMigrationAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const migration = yield* SessionMigration

		const needsMigration = yield* migration.needsMigration
		if (needsMigration) {
			yield* Effect.log("Legacy tmux sessions detected - running migration...")
			const result = yield* migration.migrate
			if (result.migratedBeads.length > 0) {
				yield* Effect.log(
					`✓ Migrated ${result.migratedBeads.length} beads to unified session format`,
				)
			}
			if (result.errors.length > 0) {
				yield* Effect.logError(`Migration errors: ${result.errors.map((e) => e.beadId).join(", ")}`)
			}
		}
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
 *        await startSession(beadId)
 */
export const startSessionAtom = appRuntime.fn((beadId: string) =>
	Effect.gen(function* () {
		const manager = yield* ClaudeSessionManager
		const ptyMonitor = yield* PTYMonitor
		const projectService = yield* ProjectService

		// Get current project path (or cwd if no project selected)
		const projectPath = (yield* projectService.getCurrentPath()) ?? process.cwd()

		const session = yield* manager.start({
			beadId,
			projectPath,
		})

		// Register with PTYMonitor for state detection
		yield* ptyMonitor.registerSession(beadId, session.tmuxSessionName)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Pause a running session (Ctrl+C + WIP commit)
 */
export const pauseSessionAtom = appRuntime.fn((beadId: string) =>
	Effect.gen(function* () {
		const manager = yield* ClaudeSessionManager
		yield* manager.pause(beadId)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Resume a paused session
 */
export const resumeSessionAtom = appRuntime.fn((beadId: string) =>
	Effect.gen(function* () {
		const manager = yield* ClaudeSessionManager
		yield* manager.resume(beadId)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Stop a running session (kills tmux, marks as idle)
 *
 * Also unregisters the session from PTYMonitor.
 */
export const stopSessionAtom = appRuntime.fn((beadId: string) =>
	Effect.gen(function* () {
		const manager = yield* ClaudeSessionManager
		const ptyMonitor = yield* PTYMonitor

		// Unregister from PTYMonitor first (before session is stopped)
		yield* ptyMonitor.unregisterSession(beadId)

		yield* manager.stop(beadId)
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
