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
import type { HandlerContext } from "./types.js"

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

			// Get current project path (from ProjectService or cwd fallback)
			const projectPath = yield* ctx.getProjectPath()

			yield* ctx.withQueue(
				task.id,
				"start",
				ctx.sessionManager.start({ beadId: task.id, projectPath }).pipe(
					Effect.tap(() => ctx.toast.show("success", `Started session for ${task.id}`)),
					Effect.catchAll(ctx.showErrorToast("Failed to start")),
				),
			)
		}),

	/**
	 * Start session with initial prompt (Space+S)
	 *
	 * Starts Claude with a detailed prompt containing the bead ID and title.
	 * If the task has attached images, their paths are included so Claude
	 * can use the Read tool to view them.
	 *
	 * This helps Claude understand that it should work on a specific beads issue.
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

			// Get current project path (from ProjectService or cwd fallback)
			const projectPath = yield* ctx.getProjectPath()

			// Build a clear prompt that explicitly identifies this as a beads issue.
			// The prompt encourages Claude to:
			// 1. Read the full bead context via `bd show`
			// 2. Ask clarifying questions if anything is unclear
			// 3. Update the bead with design/acceptance criteria for future sessions
			//
			// This ensures beads become self-sufficient over time - any Claude session
			// can pick them up without extra research or context from the user.
			let initialPrompt = `work on bead ${task.id} (${task.issue_type}): ${task.title}

Before starting implementation:
1. Run \`bd show ${task.id}\` to read the full description, design notes, and acceptance criteria
2. If ANYTHING is unclear or underspecified, ASK ME questions before proceeding
3. Once you understand the task, update the bead with your implementation plan using \`bd update ${task.id} --design="..."\`

Goal: Make this bead self-sufficient so any future session could pick it up without extra context.`

			// Check for attached images and include their paths
			// This allows Claude to use the Read tool to view them
			const attachments = yield* ctx.imageAttachment
				.list(task.id)
				.pipe(Effect.catchAll(() => Effect.succeed([] as const)))
			if (attachments.length > 0) {
				const imagePaths = attachments.map(
					(a) => `${projectPath}/.beads/images/${task.id}/${a.filename}`,
				)
				initialPrompt += `\n\nAttached images (use Read tool to view):\n${imagePaths.join("\n")}`
			}

			yield* ctx.withQueue(
				task.id,
				"start",
				ctx.sessionManager
					.start({
						beadId: task.id,
						projectPath,
						initialPrompt,
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
	 * Start session with prompt and --dangerously-skip-permissions (Space+!)
	 *
	 * Starts Claude with a detailed prompt AND the --dangerously-skip-permissions flag.
	 * This allows Claude to run without permission prompts - useful for trusted tasks
	 * but should be used with caution.
	 * Queued to prevent race conditions with other operations on the same task.
	 * Blocked if task already has an operation in progress.
	 */
	startSessionDangerous: () =>
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

			// Get current project path
			const projectPath = yield* ctx.getProjectPath()

			// Build prompt (same as startSessionWithPrompt)
			// The prompt encourages Claude to:
			// 1. Read the full bead context via `bd show`
			// 2. Ask clarifying questions if anything is unclear
			// 3. Update the bead with design/acceptance criteria for future sessions
			let initialPrompt = `work on bead ${task.id} (${task.issue_type}): ${task.title}

Before starting implementation:
1. Run \`bd show ${task.id}\` to read the full description, design notes, and acceptance criteria
2. If ANYTHING is unclear or underspecified, ASK ME questions before proceeding
3. Once you understand the task, update the bead with your implementation plan using \`bd update ${task.id} --design="..."\`

Goal: Make this bead self-sufficient so any future session could pick it up without extra context.`

			// Check for attached images and include their paths
			const attachments = yield* ctx.imageAttachment
				.list(task.id)
				.pipe(Effect.catchAll(() => Effect.succeed([] as const)))
			if (attachments.length > 0) {
				const imagePaths = attachments.map(
					(a) => `${projectPath}/.beads/images/${task.id}/${a.filename}`,
				)
				initialPrompt += `\n\nAttached images (use Read tool to view):\n${imagePaths.join("\n")}`
			}

			yield* ctx.withQueue(
				task.id,
				"start",
				ctx.sessionManager
					.start({
						beadId: task.id,
						projectPath,
						initialPrompt,
						dangerouslySkipPermissions: true,
					})
					.pipe(
						Effect.tap(() =>
							ctx.toast.show("success", `Started session for ${task.id} (skip-permissions)`),
						),
						Effect.catchAll(ctx.showErrorToast("Failed to start")),
					),
			)
		}),

	/**
	 * Chat about task (Space+c)
	 *
	 * Spawns a Haiku chat session with worktree (like Space+S but with Haiku model).
	 * Creates worktree so you can seamlessly transition from discussion to implementation.
	 * Uses Haiku model for faster, cheaper responses during exploration/discussion.
	 */
	chatAboutTask: () =>
		Effect.gen(function* () {
			const task = yield* ctx.getSelectedTask()
			if (!task) return

			// Check if task has an operation in progress
			const isBusy = yield* ctx.checkBusy(task.id)
			if (isBusy) return

			// If task already has a session, just attach to it
			if (task.sessionState !== "idle") {
				yield* ctx.attachment.attachExternal(task.id).pipe(
					Effect.tap(() => ctx.toast.show("info", "Attached to existing session")),
					Effect.catchAll(ctx.showErrorToast("Failed to attach")),
				)
				return
			}

			// Get current project path
			const projectPath = yield* ctx.getProjectPath()

			// Build chat-focused prompt (different from work prompt)
			// Structured to help Claude understand what chat mode is for
			const initialPrompt = `Let's chat about bead ${task.id}: ${task.title}

Run \`bd show ${task.id}\` to see the current state.

Help me with one of:
- Clarifying requirements or scope
- Improving the description so any Claude session could pick it up
- Breaking down into subtasks if too large
- Adding acceptance criteria
- Just chatting about the task or exploring ideas

Note: You're running with Haiku for fast, cheap discussion. When ready to implement, use \`/model sonnet\` to switch.

What would you like to discuss?`

			// Use regular start() with Haiku model - creates worktree like Space+S
			// This allows seamless transition from chat to implementation
			yield* ctx.withQueue(
				task.id,
				"start",
				ctx.sessionManager
					.start({
						beadId: task.id,
						projectPath,
						initialPrompt,
						model: "haiku", // Use Haiku for faster/cheaper discussion
					})
					.pipe(
						Effect.tap(() =>
							ctx.toast.show("success", `Chat session started for ${task.id} (Haiku)`),
						),
						Effect.catchAll(ctx.showErrorToast("Failed to start chat session")),
					),
			)
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
