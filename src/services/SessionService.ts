/**
 * SessionService - Facade for Claude session orchestration
 *
 * This service consolidates scattered session logic into a single API layer.
 * It acts as a facade over:
 * - SessionManager (spawning/stopping Claude sessions)
 * - AttachmentService (attaching to tmux sessions)
 * - PRWorkflow (auto-PR creation on completion)
 * - BeadsClient (board state updates)
 *
 * KeyboardService delegates to SessionService for session operations,
 * keeping UI event routing separate from business logic.
 */

import { Effect } from "effect"
import { AttachmentService } from "../core/AttachmentService"
import { BeadsClient } from "../core/BeadsClient"
import { PRWorkflow } from "../core/PRWorkflow"
import { type ClaudeModel, SessionManager } from "../core/SessionManager"
import { BoardService } from "./BoardService"
import { NavigationService } from "./NavigationService"
import { ToastService } from "./ToastService"

// ============================================================================
// Type Definitions
// ============================================================================

/**
 * Options for spawning a new session
 */
export interface SpawnOptions {
	/** The bead/task ID to work on */
	readonly taskId: string
	/** Optional initial prompt to send to Claude (e.g., "work on az-123") */
	readonly initialPrompt?: string
	/** Optional model to use (haiku, sonnet, opus). Uses Claude default if not specified. */
	readonly model?: ClaudeModel
}

/**
 * Options for attaching to a session
 */
export interface AttachOptions {
	/** The bead/task ID of the session to attach to */
	readonly taskId: string
	/** Attachment mode: external (new terminal) or inline (replace TUI) */
	readonly mode: "external" | "inline"
}

/**
 * Options for session completion handling
 */
export interface OnCompleteOptions {
	/** The bead/task ID that completed */
	readonly taskId: string
	/** Whether to auto-create a PR (default: false) */
	readonly createPR?: boolean
	/** Whether to close the bead issue (default: false) */
	readonly closeBead?: boolean
}

// ============================================================================
// Service Definition
// ============================================================================

export class SessionService extends Effect.Service<SessionService>()("SessionService", {
	dependencies: [
		ToastService.Default,
		NavigationService.Default,
		SessionManager.Default,
		AttachmentService.Default,
		PRWorkflow.Default,
		BeadsClient.Default,
		BoardService.Default,
	],

	effect: Effect.gen(function* () {
		const toast = yield* ToastService
		const navigation = yield* NavigationService
		const sessionManager = yield* SessionManager
		const attachment = yield* AttachmentService
		const prWorkflow = yield* PRWorkflow
		const beadsClient = yield* BeadsClient
		const board = yield* BoardService

		/**
		 * Helper to show error toasts with logging
		 */
		const showErrorToast =
			(prefix: string) =>
			(error: unknown): Effect.Effect<void> => {
				const message =
					error && typeof error === "object" && "message" in error
						? String((error as { message: string }).message)
						: String(error)
				return Effect.gen(function* () {
					yield* Effect.logError(`${prefix}: ${message}`, { error })
					yield* toast.show("error", `${prefix}: ${message}`)
				})
			}

		return {
			/**
			 * Spawn a new Claude session for a task
			 *
			 * Creates a git worktree, spawns a tmux session with Claude,
			 * shows a toast notification, and enables follow mode for the task.
			 *
			 * Consolidates the logic from:
			 * - KeyboardService actionStartSession (Space+s)
			 * - KeyboardService actionStartSessionWithPrompt (Space+S)
			 *
			 * @param options - Spawn options (taskId, initialPrompt, model)
			 * @returns Effect that resolves on success (errors are caught and shown as toasts)
			 */
			spawn: (options: SpawnOptions) =>
				Effect.gen(function* () {
					const { taskId, initialPrompt, model } = options

					yield* sessionManager
						.start({
							beadId: taskId,
							projectPath: process.cwd(),
							initialPrompt,
							model,
						})
						.pipe(
							Effect.tap(() => {
								const promptInfo = initialPrompt ? " with prompt" : ""
								return toast.show("success", `Started session for ${taskId}${promptInfo}`)
							}),
							Effect.tap(() => navigation.setFollow(taskId)),
							Effect.catchAll(showErrorToast("Failed to start session")),
						)
				}),

			/**
			 * Attach to an existing Claude session
			 *
			 * Attaches the user's terminal to the tmux session for manual
			 * intervention. Supports external (new terminal) or inline (replace TUI) modes.
			 *
			 * Consolidates the logic from:
			 * - KeyboardService actionAttachExternal (Space+a)
			 * - KeyboardService actionAttachInline (Space+A)
			 *
			 * @param options - Attach options (taskId, mode)
			 * @returns Effect that resolves on success (errors are caught and shown as toasts)
			 */
			attach: (options: AttachOptions) =>
				Effect.gen(function* () {
					const { taskId, mode } = options

					if (mode === "external") {
						yield* attachment.attachExternal(taskId).pipe(
							Effect.tap(() => toast.show("info", "Switched! Ctrl-a Ctrl-a to return")),
							Effect.catchAll((error) => {
								const msg =
									error && typeof error === "object" && "_tag" in error
										? error._tag === "SessionNotFoundError"
											? `No session for ${taskId} - press Space+s to start`
											: String((error as { message?: string }).message || error)
										: String(error)
								return Effect.gen(function* () {
									yield* Effect.logError(`Attach external: ${msg}`, { error })
									yield* toast.show("error", msg)
								})
							}),
						)
					} else {
						yield* attachment
							.attachInline(taskId)
							.pipe(Effect.catchAll(showErrorToast("Failed to attach")))
					}
				}),

			/**
			 * Handle session completion
			 *
			 * Called when a Claude session completes successfully.
			 * Can optionally create a PR and/or close the bead issue.
			 *
			 * This is NEW functionality - enables automatic PR workflow
			 * when sessions complete.
			 *
			 * @param options - Completion options (taskId, createPR, closeBead)
			 * @returns Effect that resolves on success (errors are caught and shown as toasts)
			 */
			onComplete: (options: OnCompleteOptions) =>
				Effect.gen(function* () {
					const { taskId, createPR = false, closeBead = false } = options

					yield* toast.show("success", `Session ${taskId} completed!`)

					// Optionally create PR
					if (createPR) {
						yield* prWorkflow
							.createPR({
								beadId: taskId,
								projectPath: process.cwd(),
							})
							.pipe(
								Effect.tap((pr) => toast.show("success", `PR created: ${pr.url}`)),
								Effect.catchAll((error) => {
									const msg =
										error &&
										typeof error === "object" &&
										"_tag" in error &&
										error._tag === "GHCLIError"
											? String((error as { message: string }).message)
											: `Failed to create PR: ${error}`
									return Effect.gen(function* () {
										yield* Effect.logError(`Create PR: ${msg}`, { error })
										yield* toast.show("error", msg)
									})
								}),
							)
					}

					// Optionally close the bead
					if (closeBead) {
						yield* beadsClient.close(taskId, "Completed by Claude session").pipe(
							Effect.tap(() => toast.show("info", `Closed ${taskId}`)),
							Effect.catchAll(showErrorToast("Failed to close bead")),
						)
					}

					// Always refresh the board to show updated state
					yield* board.refresh()
				}),
		}
	}),
}) {}
