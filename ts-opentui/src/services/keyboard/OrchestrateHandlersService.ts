/**
 * OrchestrateHandlersService
 *
 * Handles orchestration mode keyboard actions:
 * - Enter orchestrate mode from detail overlay (o)
 * - Confirm spawn selected tasks (enter)
 *
 * Orchestrate mode allows parallel session spawning for epic child tasks.
 * Converted from factory pattern to Effect.Service layer.
 */

import { Effect, Option } from "effect"
import { BeadsClient } from "../../core/BeadsClient.js"
import { ClaudeSessionManager } from "../../core/ClaudeSessionManager.js"
import { TemplateService } from "../../core/TemplateService.js"
import type { OrchestrationTask } from "../EditorService.js"
import { EditorService } from "../EditorService.js"
import { OverlayService } from "../OverlayService.js"
import { ToastService } from "../ToastService.js"
import { KeyboardHelpersService } from "./KeyboardHelpersService.js"

// ============================================================================
// Service Definition
// ============================================================================

export class OrchestrateHandlersService extends Effect.Service<OrchestrateHandlersService>()(
	"OrchestrateHandlersService",
	{
		dependencies: [
			KeyboardHelpersService.Default,
			ToastService.Default,
			EditorService.Default,
			OverlayService.Default,
			BeadsClient.Default,
			ClaudeSessionManager.Default,
			TemplateService.Default,
		],

		effect: Effect.gen(function* () {
			// Inject services at construction time
			const helpers = yield* KeyboardHelpersService
			const toast = yield* ToastService
			const editor = yield* EditorService
			const overlay = yield* OverlayService
			const beads = yield* BeadsClient
			const sessionManager = yield* ClaudeSessionManager
			const templateService = yield* TemplateService

			// ================================================================
			// Orchestrate Handler Methods
			// ================================================================

			/**
			 * Enter orchestrate mode from detail overlay (o key)
			 *
			 * Validates that the current detail overlay task is an epic,
			 * fetches its child tasks, and enters orchestrate mode.
			 * Shows an error toast if the task is not an epic.
			 */
			const enterFromDetail = () =>
				Effect.gen(function* () {
					// Get the current overlay
					const current = yield* overlay.current()
					if (!current || current._tag !== "detail") {
						yield* toast.show("error", "No task detail open")
						return
					}

					// Get the task details
					const task = yield* beads.show(current.taskId).pipe(
						Effect.catchAll((error) => {
							const msg =
								error && typeof error === "object" && "_tag" in error
									? error._tag === "NotFoundError"
										? `Task ${current.taskId} not found`
										: `Failed to load task: ${error}`
									: `Failed to load task: ${error}`
							return Effect.gen(function* () {
								yield* Effect.logError(`Enter orchestrate: ${msg}`, { error })
								yield* toast.show("error", msg)
								return yield* Effect.fail(error)
							})
						}),
					)

					// Validate that this is an epic
					if (task.issue_type !== "epic") {
						yield* toast.show("error", "Only epics can be orchestrated")
						return
					}

					// Fetch the epic with its children
					const epicWithChildren = yield* beads.getEpicWithChildren(task.id).pipe(
						Effect.catchAll((error) =>
							Effect.gen(function* () {
								const msg =
									error && typeof error === "object" && "_tag" in error
										? `Failed to load epic children: ${error._tag}`
										: `Failed to load epic children: ${error}`
								yield* Effect.logError(msg, { error })
								yield* toast.show("error", msg)
								return yield* Effect.fail(error)
							}),
						),
					)

					// Get all active sessions to determine hasSession state
					const activeSessions = yield* sessionManager
						.listActive()
						.pipe(Effect.catchAll(() => Effect.succeed([] as const)))
					const activeSessionIds = new Set(activeSessions.map((s) => s.beadId))

					// Map children to OrchestrationTask format (filter out tombstones)
					const orchestrationTasks: ReadonlyArray<OrchestrationTask> = epicWithChildren.children
						.filter((child) => child.status !== "tombstone")
						.map((child) => ({
							id: child.id,
							title: child.title,
							status: child.status as "open" | "in_progress" | "blocked" | "closed",
							hasSession: activeSessionIds.has(child.id),
						}))

					// Enter orchestrate mode
					yield* editor.enterOrchestrate(task.id, task.title, orchestrationTasks)

					// Close the detail overlay
					yield* overlay.pop()
				})

			/**
			 * Confirm spawn selected tasks (enter key in orchestrate mode)
			 *
			 * Spawns Claude sessions for all selected tasks in orchestrate mode.
			 * Each session is injected with epic context via the worker template.
			 * Only spawns tasks that are in "open" status and don't already have sessions.
			 * Exits orchestrate mode after spawning.
			 */
			const confirmSpawn = () =>
				Effect.gen(function* () {
					// Get current mode and validate it's orchestrate
					const mode = yield* editor.getMode()
					if (mode._tag !== "orchestrate") {
						yield* toast.show("error", "Not in orchestrate mode")
						return
					}

					// Check if any tasks are selected
					if (mode.selectedIds.length === 0) {
						yield* toast.show("error", "No tasks selected")
						return
					}

					// Get project path for spawning sessions
					const projectPath = yield* helpers.getProjectPath()

					// Load epic details for context injection
					const epic = yield* beads
						.show(mode.epicId)
						.pipe(Effect.catchAll(() => Effect.succeed(null)))

					// Exit orchestrate mode first (so UI updates)
					yield* editor.exitOrchestrate()

					// Spawn sessions for each selected task with epic context
					// Use Effect.all to spawn all sessions in parallel
					const spawnResults = yield* Effect.all(
						mode.selectedIds.map((taskId) =>
							Effect.gen(function* () {
								// Load task details for template
								const task = yield* beads
									.show(taskId)
									.pipe(Effect.catchAll(() => Effect.succeed(null)))

								// Try to render worker template with context
								const initialPrompt = yield* templateService
									.tryRenderWorkerTemplate(
										{
											TASK_ID: taskId,
											TASK_TITLE: task?.title ?? taskId,
											TASK_DESCRIPTION: task?.description,
											TASK_DESIGN: task?.design,
											EPIC_ID: mode.epicId,
											EPIC_TITLE: mode.epicTitle,
											EPIC_DESIGN: epic?.design,
										},
										projectPath,
									)
									.pipe(
										Effect.map((opt) =>
											Option.isSome(opt) ? opt.value : `Work on bead ${taskId}`,
										),
									)

								// Start session with rendered template as initial prompt
								return yield* sessionManager
									.start({
										beadId: taskId,
										projectPath,
										initialPrompt,
									})
									.pipe(
										Effect.tap(() => Effect.logInfo(`Spawned session for ${taskId}`)),
										// Catch individual spawn failures so one failure doesn't block others
										Effect.catchAll((error) => {
											const msg =
												error && typeof error === "object" && "message" in error
													? String(error.message)
													: String(error)
											return Effect.gen(function* () {
												yield* Effect.logError(`Failed to spawn ${taskId}: ${msg}`, {
													error,
												})
												return yield* Effect.succeed(undefined)
											})
										}),
									)
							}),
						),
					)

					// Count successful spawns
					const successCount = spawnResults.filter((result) => result !== undefined).length

					// Show toast with spawn count
					if (successCount === mode.selectedIds.length) {
						yield* toast.show(
							"success",
							`Spawned ${successCount} session${successCount === 1 ? "" : "s"}`,
						)
					} else {
						yield* toast.show(
							"error",
							`Spawned ${successCount}/${mode.selectedIds.length} sessions (some failed)`,
						)
					}
				})

			// ================================================================
			// Public API
			// ================================================================

			return {
				enterFromDetail,
				confirmSpawn,
			}
		}),
	},
) {}
