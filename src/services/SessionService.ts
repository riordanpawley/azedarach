/**
 * SessionService - Claude session orchestration
 *
 * Manages Claude Code sessions in git worktrees using Effect.Service pattern.
 * Coordinates with ToastService for notifications and NavigationService for
 * cursor follow mode when spawning sessions.
 */

import { Effect } from "effect"
import { ClaudeSessionManager } from "../core/ClaudeSessionManager.js"
import { PTYMonitor } from "../core/PTYMonitor.js"
import { TmuxSessionMonitor } from "../core/TmuxSessionMonitor.js"
import type { SessionState } from "../ui/types.js"
import { NavigationService } from "./NavigationService.js"
import { ToastService } from "./ToastService.js"

// ============================================================================
// Service Definition
// ============================================================================

export class SessionService extends Effect.Service<SessionService>()("SessionService", {
	dependencies: [
		ToastService.Default,
		NavigationService.Default,
		TmuxSessionMonitor.Default,
		PTYMonitor.Default,
		ClaudeSessionManager.Default,
	],

	scoped: Effect.gen(function* () {
		const toast = yield* ToastService
		const navigation = yield* NavigationService
		const tmuxMonitor = yield* TmuxSessionMonitor
		const ptyMonitor = yield* PTYMonitor

		yield* tmuxMonitor.start((update) =>
			Effect.gen(function* () {
				const state: SessionState = update.status === "idle" ? "idle" : update.status
				yield* ptyMonitor.recordHookSignal(update.beadId, state)
			}),
		)

		return {
			/**
			 * Spawn a new Claude session for the given task
			 *
			 * Creates a git worktree, spawns a tmux session with Claude,
			 * shows a toast notification, and enables follow mode for the task.
			 *
			 * @param taskId - The task/bead ID to spawn a session for
			 */
			spawn: (taskId: string): Effect.Effect<void> =>
				Effect.gen(function* () {
					// TODO: Implement worktree creation and tmux session spawning
					yield* toast.show("info", `Spawning session for ${taskId}`)
					yield* navigation.setFollow(taskId)
				}),

			/**
			 * Attach to an existing Claude session
			 *
			 * Attaches the user's terminal to the tmux session for manual
			 * intervention. Shows a toast notification.
			 *
			 * @param taskId - The task/bead ID of the session to attach to
			 */
			attach: (taskId: string): Effect.Effect<void> =>
				Effect.gen(function* () {
					// TODO: Implement tmux session attachment
					yield* toast.show("info", `Attaching to ${taskId}`)
				}),

			/**
			 * Handle session completion
			 *
			 * Called when a Claude session completes successfully.
			 * Shows a success toast and can trigger PR workflow or
			 * other post-completion actions.
			 *
			 * @param taskId - The task/bead ID of the completed session
			 */
			onComplete: (taskId: string): Effect.Effect<void> =>
				Effect.gen(function* () {
					// TODO: Implement PR workflow, board updates, etc.
					yield* toast.show("success", `Session ${taskId} completed!`)
				}),
		}
	}),
}) {}
