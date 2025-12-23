/**
 * SessionHandlersService
 *
 * Handles Claude session lifecycle:
 * - Start session (s) / Start with prompt (S)
 * - Chat about task (c)
 * - Attach external (a) / Attach inline (A)
 * - Pause (p) / Resume (r)
 * - Stop (x)
 *
 * Converted from factory pattern to Effect.Service layer.
 */

import { Effect } from "effect"
import { AppConfig } from "../../config/index.js"
import { AttachmentService } from "../../core/AttachmentService.js"
import { ClaudeSessionManager } from "../../core/ClaudeSessionManager.js"
import { ImageAttachmentService } from "../../core/ImageAttachmentService.js"
import { PRWorkflow } from "../../core/PRWorkflow.js"
import { AI_SESSION_PREFIXES } from "../../core/paths.js"
import { escapeForShellDoubleQuotes } from "../../core/shell.js"
import { TmuxService } from "../../core/TmuxService.js"
import { BoardService } from "../BoardService.js"
import { OverlayService } from "../OverlayService.js"
import { ToastService } from "../ToastService.js"
import { KeyboardHelpersService } from "./KeyboardHelpersService.js"

// ============================================================================
// Service Definition
// ============================================================================

export class SessionHandlersService extends Effect.Service<SessionHandlersService>()(
	"SessionHandlersService",
	{
		dependencies: [
			KeyboardHelpersService.Default,
			ToastService.Default,
			ClaudeSessionManager.Default,
			AttachmentService.Default,
			ImageAttachmentService.Default,
			TmuxService.Default,
			AppConfig.Default,
			PRWorkflow.Default,
			OverlayService.Default,
			BoardService.Default,
		],

		effect: Effect.gen(function* () {
			// Inject services at construction time
			const helpers = yield* KeyboardHelpersService
			const toast = yield* ToastService
			const sessionManager = yield* ClaudeSessionManager
			const attachment = yield* AttachmentService
			const imageAttachment = yield* ImageAttachmentService
			const tmux = yield* TmuxService
			const appConfig = yield* AppConfig
			const prWorkflow = yield* PRWorkflow
			const overlay = yield* OverlayService
			const boardService = yield* BoardService
			const gitConfig = yield* appConfig.getGitConfig()

			// ================================================================
			// Session Handler Methods
			// ================================================================

			/**
			 * Start session action (Space+s)
			 *
			 * Starts a Claude session for the currently selected task.
			 * Queued to prevent race conditions with other operations on the same task.
			 * Blocked if task already has an operation in progress.
			 */
			const startSession = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) return

					// Check if task has an operation in progress
					const isBusy = yield* helpers.checkBusy(task.id)
					if (isBusy) return

					if (task.sessionState !== "idle") {
						yield* toast.show("error", `Cannot start: task is ${task.sessionState}`)
						return
					}

					// Get current project path (from ProjectService or cwd fallback)
					const projectPath = yield* helpers.getProjectPath()

					yield* helpers.withQueue(
						task.id,
						"start",
						sessionManager.start({ beadId: task.id, projectPath }).pipe(
							Effect.tap(() => toast.show("success", `Started session for ${task.id}`)),
							Effect.catchAll(helpers.showErrorToast("Failed to start")),
						),
					)
				})

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
			const startSessionWithPrompt = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) return

					// Check if task has an operation in progress
					const isBusy = yield* helpers.checkBusy(task.id)
					if (isBusy) return

					if (task.sessionState !== "idle") {
						yield* toast.show("error", `Cannot start: task is ${task.sessionState}`)
						return
					}

					// Get current project path (from ProjectService or cwd fallback)
					const projectPath = yield* helpers.getProjectPath()

					// Build a clear prompt that explicitly identifies this as a beads issue.
					// We only provide the ID and title, and tell Claude to run `bd show`
					// to get the full context.
					//
					// The prompt encourages Claude to:
					// 1. Ask clarifying questions if anything is unclear
					// 2. Update the bead with design notes for future sessions
					//
					// This ensures beads become self-sufficient over time - any Claude session
					// can pick them up without extra research or context from the user.
					let initialPrompt = `work on bead ${task.id} (${task.issue_type}): ${task.title}

Run \`bd show ${task.id}\` to see full description and context.

Before starting implementation:
1. If ANYTHING is unclear or underspecified, ASK ME questions before proceeding
2. Once you understand the task, update the bead with your implementation plan using \`bd update ${task.id} --design="..."\`

Goal: Make this bead self-sufficient so any future session could pick it up without extra context.`

					// Check for attached images and include their paths
					// This allows Claude to use the Read tool to view them
					const attachments = yield* imageAttachment
						.list(task.id)
						.pipe(Effect.catchAll(() => Effect.succeed([] as const)))
					if (attachments.length > 0) {
						const imagePaths = attachments.map(
							(a) => `${projectPath}/.beads/images/${task.id}/${a.filename}`,
						)
						initialPrompt += `\n\nAttached images (use Read tool to view):\n${imagePaths.join("\n")}`
					}

					yield* helpers.withQueue(
						task.id,
						"start",
						sessionManager
							.start({
								beadId: task.id,
								projectPath,
								initialPrompt,
							})
							.pipe(
								Effect.tap(() =>
									toast.show("success", `Started session for ${task.id} with prompt`),
								),
								Effect.catchAll(helpers.showErrorToast("Failed to start")),
							),
					)
				})

			/**
			 * Start session with prompt and --dangerously-skip-permissions (Space+!)
			 *
			 * Starts Claude with a detailed prompt AND the --dangerously-skip-permissions flag.
			 * This allows Claude to run without permission prompts - useful for trusted tasks
			 * but should be used with caution.
			 * Queued to prevent race conditions with other operations on the same task.
			 * Blocked if task already has an operation in progress.
			 */
			const startSessionDangerous = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) return

					// Check if task has an operation in progress
					const isBusy = yield* helpers.checkBusy(task.id)
					if (isBusy) return

					if (task.sessionState !== "idle") {
						yield* toast.show("error", `Cannot start: task is ${task.sessionState}`)
						return
					}

					// Get current project path
					const projectPath = yield* helpers.getProjectPath()

					// Build prompt (same as startSessionWithPrompt)
					// We only provide the ID and title, and tell Claude to run `bd show`
					// to get the full context.
					let initialPrompt = `work on bead ${task.id} (${task.issue_type}): ${task.title}

Run \`bd show ${task.id}\` to see full description and context.

Before starting implementation:
1. If ANYTHING is unclear or underspecified, ASK ME questions before proceeding
2. Once you understand the task, update the bead with your implementation plan using \`bd update ${task.id} --design="..."\`

Goal: Make this bead self-sufficient so any future session could pick it up without extra context.`

					// Check for attached images and include their paths
					const attachments = yield* imageAttachment
						.list(task.id)
						.pipe(Effect.catchAll(() => Effect.succeed([] as const)))
					if (attachments.length > 0) {
						const imagePaths = attachments.map(
							(a) => `${projectPath}/.beads/images/${task.id}/${a.filename}`,
						)
						initialPrompt += `\n\nAttached images (use Read tool to view):\n${imagePaths.join("\n")}`
					}

					yield* helpers.withQueue(
						task.id,
						"start",
						sessionManager
							.start({
								beadId: task.id,
								projectPath,
								initialPrompt,
								dangerouslySkipPermissions: true,
							})
							.pipe(
								Effect.tap(() =>
									toast.show("success", `Started session for ${task.id} (skip-permissions)`),
								),
								Effect.catchAll(helpers.showErrorToast("Failed to start")),
							),
					)
				})

			/**
			 * Chat about task (Space+c)
			 *
			 * Spawns a Haiku chat in a dedicated tmux session to discuss/understand the task.
			 * Unlike startSession, this runs in the current project directory (not a worktree).
			 * Session is created in the background - user remains in Azedarach TUI.
			 * Uses Haiku model for faster, cheaper responses.
			 *
			 * The session name is `chat-<beadId>` to distinguish from work sessions.
			 * User can attach to the chat session via Space+a (attach external).
			 */
			const chatAboutTask = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) return

					// Build the Claude command with specified chat model and initial prompt
					const sessionConfig = yield* appConfig.getSessionConfig()
					const cliTool = yield* appConfig.getCliTool()
					const modelConfig = yield* appConfig.getModelConfig()
					const { command: cliCommand, shell, tmuxPrefix } = sessionConfig

					const toolModelConfig = cliTool === "claude" ? modelConfig.claude : modelConfig.opencode
					const chatModel =
						modelConfig.chat ??
						toolModelConfig.chat ??
						modelConfig.default ??
						toolModelConfig.default ??
						"haiku"

					// Inject bead context directly for chat sessions too
					const prompt = `Let's chat about bead ${task.id}: ${task.title}

Run \`bd show ${task.id}\` to see full description and context.

Help me with one of:
- Clarifying requirements or scope
- Improving the description so any Claude session could pick it up
- Breaking down into subtasks if too large
- Adding acceptance criteria
- Just chatting about the task or exploring ideas

Note: You're running with ${chatModel} for fast, cheap discussion. When ready to implement, use \`/model <model>\` to switch models.

What would you like to discuss?`
					const fullCommand = `${cliCommand} --model ${chatModel} "${escapeForShellDoubleQuotes(prompt)}"`

					// Get project path for session cwd
					const projectPath = yield* helpers.getProjectPath()

					// Use chat-{beadId} naming to distinguish from work sessions
					const chatSessionName = `chat-${task.id}`

					// Check if chat session already exists
					const hasSession = yield* tmux.hasSession(chatSessionName)

					if (hasSession) {
						// Session already exists - inform user they can attach
						yield* toast.show(
							"info",
							`Chat session already exists for ${task.id} - press Space+a to attach`,
						)
					} else {
						// Create new chat session in background
						// Use interactive shell so we can resume if Claude exits
						yield* tmux
							.newSession(chatSessionName, {
								cwd: projectPath,
								command: `${shell} -i -c '${fullCommand}; exec ${shell}'`,
								prefix: tmuxPrefix,
							})
							.pipe(
								Effect.tap(() =>
									toast.show(
										"success",
										`Chat session started for ${task.id} - press Space+a to attach`,
									),
								),
								Effect.catchAll(helpers.showErrorToast("Failed to start chat session")),
							)
					}
				})

			/**
			 * Find AI session by beadId (checks both claude- and opencode- prefixes)
			 */
			const findAiSession = (beadId: string) =>
				Effect.gen(function* () {
					for (const prefix of AI_SESSION_PREFIXES) {
						const sessionName = `${prefix}${beadId}`
						const hasSession = yield* tmux.hasSession(sessionName)
						if (hasSession) {
							return sessionName
						}
					}
					return null
				})

			/**
			 * Perform the actual attach action
			 *
			 * Internal helper that does the tmux switch.
			 * Takes beadId and searches for the session with either prefix.
			 */
			const doAttach = (beadId: string) =>
				Effect.gen(function* () {
					const sessionName = yield* findAiSession(beadId)
					if (!sessionName) {
						yield* toast.show("error", `No session for ${beadId} - press Space+s to start`)
						return
					}
					yield* attachment.attachExternal(sessionName)
					yield* toast.show("info", "Switched! Ctrl-a Ctrl-a to return")
				}).pipe(
					Effect.catchAll((error) => {
						const msg =
							error && typeof error === "object" && "_tag" in error
								? String((error as { message?: string }).message || error)
								: String(error)
						return Effect.gen(function* () {
							yield* Effect.logError(`Attach external: ${msg}`, { error })
							yield* toast.show("error", msg)
						})
					}),
				)

			/**
			 * Attach to session externally (Space+a)
			 *
			 * Switches to the tmux session in a new terminal window.
			 * After successful attach, checks for PR comments and injects them into the session.
			 * The user can return with Ctrl-a Ctrl-a.
			 *
			 * If the worktree branch is behind main, shows a choice dialog:
			 * - Merge & Attach: merges main into branch, then attaches
			 * - Skip & Attach: attaches without merging
			 * - Cancel: returns to board
			 *
			 * If merge has conflicts, spawns Claude to resolve them.
			 */
			const attachExternal = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) return

					// Get current project path
					const projectPath = yield* helpers.getProjectPath()

					// Check if branch is behind main
					const branchStatus = yield* prWorkflow
						.checkBranchBehindMain({ beadId: task.id, projectPath })
						.pipe(Effect.catchAll(() => Effect.succeed({ behind: 0, ahead: 0 })))

					// If not behind, just attach directly
					if (branchStatus.behind === 0) {
						yield* doAttach(task.id)
						return
					}

					// Branch is behind - show merge choice dialog
					const baseBranch = gitConfig.baseBranch
					const message = `Merge ${baseBranch} into your branch before attaching?`

					// Define the merge action (merge base branch, then attach)
					const onMerge = Effect.gen(function* () {
						yield* toast.show("info", `Merging ${baseBranch} into branch...`)
						yield* prWorkflow.mergeMainIntoBranch({ beadId: task.id, projectPath }).pipe(
							Effect.tap(() => toast.show("success", "Merged! Attaching...")),
							Effect.tap(() => boardService.refresh()),
							Effect.tap(() => doAttach(task.id)),
							Effect.catchAll((error) => {
								// MergeConflictError means Claude was started to resolve
								const msg =
									error && typeof error === "object" && "_tag" in error
										? error._tag === "MergeConflictError"
											? String((error as { message?: string }).message || "Conflicts detected")
											: String((error as { message?: string }).message || error)
										: String(error)
								return Effect.gen(function* () {
									yield* Effect.logError(`Merge failed: ${msg}`, { error })
									yield* toast.show("error", msg)
								})
							}),
						)
					})

					// Define the skip action (attach directly without merging)
					const onSkip = doAttach(task.id)

					// Show the merge choice dialog
					yield* overlay.push({
						_tag: "mergeChoice",
						message,
						commitsBehind: branchStatus.behind,
						baseBranch,
						onMerge,
						onSkip,
					})
				})

			/**
			 * Attach to session inline (Space+A)
			 *
			 * Embeds the tmux session output in the current TUI.
			 */
			const attachInline = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) return

					yield* attachment
						.attachInline(task.id)
						.pipe(Effect.catchAll(helpers.showErrorToast("Failed to attach")))
				})

			/**
			 * Pause session action (Space+p)
			 *
			 * Pauses an active Claude session. Only valid when session is busy.
			 */
			const pauseSession = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) return

					if (task.sessionState !== "busy") {
						yield* toast.show("error", `Cannot pause: task is ${task.sessionState}`)
						return
					}

					yield* sessionManager.pause(task.id).pipe(
						Effect.tap(() => toast.show("success", `Paused session for ${task.id}`)),
						Effect.catchAll(helpers.showErrorToast("Failed to pause")),
					)
				})

			/**
			 * Resume session action (Space+r)
			 *
			 * Resumes a paused Claude session. Only valid when session is paused.
			 */
			const resumeSession = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) return

					if (task.sessionState !== "paused") {
						yield* toast.show("error", `Cannot resume: task is ${task.sessionState}`)
						return
					}

					yield* sessionManager.resume(task.id).pipe(
						Effect.tap(() => toast.show("success", `Resumed session for ${task.id}`)),
						Effect.catchAll(helpers.showErrorToast("Failed to resume")),
					)
				})

			/**
			 * Stop session action (Space+x)
			 *
			 * Stops a running Claude session and cleans up resources.
			 * Queued to prevent race conditions with other operations on the same task.
			 * Blocked if task already has an operation in progress.
			 */
			const stopSession = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) return

					// Check if task has an operation in progress
					const isBusy = yield* helpers.checkBusy(task.id)
					if (isBusy) return

					if (task.sessionState === "idle") {
						yield* toast.show("error", "No session to stop")
						return
					}

					yield* helpers.withQueue(
						task.id,
						"stop",
						sessionManager.stop(task.id).pipe(
							Effect.tap(() => toast.show("success", `Stopped session for ${task.id}`)),
							Effect.catchAll(helpers.showErrorToast("Failed to stop")),
						),
					)
				})

			// ================================================================
			// Public API
			// ================================================================

			return {
				startSession,
				startSessionWithPrompt,
				startSessionDangerous,
				chatAboutTask,
				attachExternal,
				attachInline,
				pauseSession,
				resumeSession,
				stopSession,
			}
		}),
	},
) {}
