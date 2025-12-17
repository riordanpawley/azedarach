/**
 * Session Key Handlers
 *
 * Handlers for Claude session lifecycle:
 * - Start session (s) / Start with prompt (S)
 * - Chat about task (c)
 * - Attach external (a) / Attach inline (A)
 * - Pause (p) / Resume (r)
 * - Stop (x)
 */

import { Effect } from "effect"
import type { HandlerContext } from "./types"

// ============================================================================
// Session Handler Factory
// ============================================================================

/**
 * Create all session-related action handlers
 *
 * These handlers manage Claude session lifecycle: starting, stopping,
 * pausing, resuming, and attaching to sessions.
 */
export const createSessionHandlers = (ctx: HandlerContext) => ({
	/**
	 * Start session action (Space+s)
	 *
	 * Starts a Claude session for the currently selected task.
	 * Queued to prevent race conditions with other operations on the same task.
	 * Blocked if task already has an operation in progress.
	 */
	startSession: () =>
		Effect.gen(function* () {
			const task = yield* ctx.getSelectedTask()
			if (!task) return

			// Check if task has an operation in progress
			const isBusy = yield* ctx.checkBusy(task.id)
			if (isBusy) return

			if (task.sessionState !== "idle") {
				yield* ctx.toast.show("error", `Cannot start: task is ${task.sessionState}`)
				return
			}

			yield* ctx.withQueue(
				task.id,
				"start",
				ctx.sessionManager.start({ beadId: task.id, projectPath: process.cwd() }).pipe(
					Effect.tap(() => ctx.toast.show("success", `Started session for ${task.id}`)),
					Effect.catchAll(ctx.showErrorToast("Failed to start")),
				),
			)
		}),

	/**
	 * Start session with initial prompt (Space+S)
	 *
	 * Starts Claude and tells it to "work on bead {beadId}".
	 * Queued to prevent race conditions with other operations on the same task.
	 * Blocked if task already has an operation in progress.
	 */
	startSessionWithPrompt: () =>
		Effect.gen(function* () {
			const task = yield* ctx.getSelectedTask()
			if (!task) return

			// Check if task has an operation in progress
			const isBusy = yield* ctx.checkBusy(task.id)
			if (isBusy) return

			if (task.sessionState !== "idle") {
				yield* ctx.toast.show("error", `Cannot start: task is ${task.sessionState}`)
				return
			}

			yield* ctx.withQueue(
				task.id,
				"start",
				ctx.sessionManager
					.start({
						beadId: task.id,
						projectPath: process.cwd(),
						initialPrompt: `work on ${task.id}`,
					})
					.pipe(
						Effect.tap(() =>
							ctx.toast.show("success", `Started session for ${task.id} with prompt`),
						),
						Effect.catchAll(ctx.showErrorToast("Failed to start")),
					),
			)
		}),

	/**
	 * Chat about task (Space+c)
	 *
	 * Opens a Haiku chat in a tmux popup to discuss/understand the task.
	 * This is an ephemeral session that runs in the current directory (not a worktree).
	 * The popup closes automatically when Claude exits.
	 * Uses Haiku model for faster, cheaper responses.
	 */
	chatAboutTask: () =>
		Effect.gen(function* () {
			const task = yield* ctx.getSelectedTask()
			if (!task) return

			// Build the Claude command with Haiku model and initial prompt
			const { command: claudeCommand } = ctx.resolvedConfig.session
			const escapeForShell = (s: string) =>
				s.replace(/\\/g, "\\\\").replace(/'/g, "'\\''").replace(/"/g, '\\"')

			const prompt = `Let's chat about ${task.id}. Help me understand this task better or improve its description/context.`
			const fullCommand = `${claudeCommand} --model haiku "${escapeForShell(prompt)}"`

			yield* ctx.tmux
				.displayPopup({
					command: fullCommand,
					width: "90%",
					height: "90%",
					title: ` Chat: ${task.id} (Haiku) - Ctrl-C to exit `,
				})
				.pipe(Effect.catchAll(ctx.showErrorToast("Failed to start chat")))
		}),

	/**
	 * Attach to session externally (Space+a)
	 *
	 * Switches to the tmux session in a new terminal window.
	 * The user can return with Ctrl-a Ctrl-a.
	 */
	attachExternal: () =>
		Effect.gen(function* () {
			const task = yield* ctx.getSelectedTask()
			if (!task) return

			yield* ctx.attachment.attachExternal(task.id).pipe(
				Effect.tap(() => ctx.toast.show("info", "Switched! Ctrl-a Ctrl-a to return")),
				Effect.catchAll((error) => {
					const msg =
						error && typeof error === "object" && "_tag" in error
							? error._tag === "SessionNotFoundError"
								? `No session for ${task.id} - press Space+s to start`
								: String((error as { message?: string }).message || error)
							: String(error)
					return Effect.gen(function* () {
						yield* Effect.logError(`Attach external: ${msg}`, { error })
						yield* ctx.toast.show("error", msg)
					})
				}),
			)
		}),

	/**
	 * Attach to session inline (Space+A)
	 *
	 * Embeds the tmux session output in the current TUI.
	 */
	attachInline: () =>
		Effect.gen(function* () {
			const task = yield* ctx.getSelectedTask()
			if (!task) return

			yield* ctx.attachment
				.attachInline(task.id)
				.pipe(Effect.catchAll(ctx.showErrorToast("Failed to attach")))
		}),

	/**
	 * Pause session action (Space+p)
	 *
	 * Pauses an active Claude session. Only valid when session is busy.
	 */
	pauseSession: () =>
		Effect.gen(function* () {
			const task = yield* ctx.getSelectedTask()
			if (!task) return

			if (task.sessionState !== "busy") {
				yield* ctx.toast.show("error", `Cannot pause: task is ${task.sessionState}`)
				return
			}

			yield* ctx.sessionManager.pause(task.id).pipe(
				Effect.tap(() => ctx.toast.show("success", `Paused session for ${task.id}`)),
				Effect.catchAll(ctx.showErrorToast("Failed to pause")),
			)
		}),

	/**
	 * Resume session action (Space+r)
	 *
	 * Resumes a paused Claude session. Only valid when session is paused.
	 */
	resumeSession: () =>
		Effect.gen(function* () {
			const task = yield* ctx.getSelectedTask()
			if (!task) return

			if (task.sessionState !== "paused") {
				yield* ctx.toast.show("error", `Cannot resume: task is ${task.sessionState}`)
				return
			}

			yield* ctx.sessionManager.resume(task.id).pipe(
				Effect.tap(() => ctx.toast.show("success", `Resumed session for ${task.id}`)),
				Effect.catchAll(ctx.showErrorToast("Failed to resume")),
			)
		}),

	/**
	 * Stop session action (Space+x)
	 *
	 * Stops a running Claude session and cleans up resources.
	 * Queued to prevent race conditions with other operations on the same task.
	 * Blocked if task already has an operation in progress.
	 */
	stopSession: () =>
		Effect.gen(function* () {
			const task = yield* ctx.getSelectedTask()
			if (!task) return

			// Check if task has an operation in progress
			const isBusy = yield* ctx.checkBusy(task.id)
			if (isBusy) return

			if (task.sessionState === "idle") {
				yield* ctx.toast.show("error", "No session to stop")
				return
			}

			yield* ctx.withQueue(
				task.id,
				"stop",
				ctx.sessionManager.stop(task.id).pipe(
					Effect.tap(() => ctx.toast.show("success", `Stopped session for ${task.id}`)),
					Effect.catchAll(ctx.showErrorToast("Failed to stop")),
				),
			)
		}),
})

export type SessionHandlers = ReturnType<typeof createSessionHandlers>
