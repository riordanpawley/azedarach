/**
 * DevServerHandlersService - Keyboard handlers for dev server actions
 *
 * Provides action handlers for dev server operations:
 * - Toggle dev server (Space+r)
 */

import { Effect } from "effect"
import { DevServerService, type NoWorktreeError } from "../DevServerService.js"
import { ProjectService } from "../ProjectService.js"
import { ToastService } from "../ToastService.js"
import { KeyboardHelpersService } from "./KeyboardHelpersService.js"

export class DevServerHandlersService extends Effect.Service<DevServerHandlersService>()(
	"DevServerHandlersService",
	{
		dependencies: [
			DevServerService.Default,
			ProjectService.Default,
			ToastService.Default,
			KeyboardHelpersService.Default,
		],
		effect: Effect.gen(function* () {
			const devServer = yield* DevServerService
			const projectService = yield* ProjectService
			const toast = yield* ToastService
			const helpers = yield* KeyboardHelpersService

			/**
			 * Toggle dev server for the currently selected bead
			 *
			 * Starts the server if stopped, stops if running.
			 * Shows toast notifications for success/error.
			 */
			const toggleDevServer = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getSelectedTask()
					if (!task) {
						yield* toast.show("error", "No task selected")
						return
					}

					const project = yield* projectService.requireCurrentProject().pipe(
						Effect.catchAll(() =>
							Effect.gen(function* () {
								yield* toast.show("error", "No project selected")
								return undefined
							}),
						),
					)
					if (!project) return

					const currentState = yield* devServer.getStatus(task.id)

					if (currentState.status === "running" || currentState.status === "starting") {
						// Stop the server
						yield* devServer.stop(task.id)
						yield* toast.show("success", `Stopped dev server for ${task.id}`)
					} else {
						// Start the server
						yield* devServer.toggle(task.id, project.path).pipe(
							Effect.tap((state) =>
								toast.show(
									"success",
									state.port
										? `Dev server running at localhost:${state.port}`
										: `Dev server starting for ${task.id}...`,
								),
							),
							Effect.catchTag("NoWorktreeError", (err: NoWorktreeError) =>
								toast.show("error", err.message),
							),
							Effect.catchTag("DevServerError", (err) => toast.show("error", err.message)),
							Effect.catchTag("TmuxError", (err) =>
								toast.show("error", `tmux error: ${err.message}`),
							),
						)
					}
				})

			return {
				toggleDevServer,
			}
		}),
	},
) {}
