/**
 * DevServerHandlersService - Keyboard handlers for dev server actions
 *
 * Provides action handlers for dev server operations:
 * - Toggle dev server (Space+r)
 * - Restart dev server (Space+Ctrl+r)
 * - Attach to dev server (Space+v)
 */

import { Effect } from "effect"
import { TmuxService } from "../../core/TmuxService.js"
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
			TmuxService.Default,
		],
		effect: Effect.gen(function* () {
			const devServer = yield* DevServerService
			const projectService = yield* ProjectService
			const toast = yield* ToastService
			const helpers = yield* KeyboardHelpersService
			const tmux = yield* TmuxService

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

			/**
			 * Restart dev server for the currently selected bead
			 *
			 * Stops the server if running, then starts it again.
			 * Only works if a dev server is currently running.
			 */
			const restartDevServer = () =>
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

					if (currentState.status !== "running" && currentState.status !== "starting") {
						yield* toast.show("error", "No dev server running to restart")
						return
					}

					// Stop then start
					yield* toast.show("info", `Restarting dev server for ${task.id}...`)
					yield* devServer.stop(task.id)

					yield* devServer.toggle(task.id, project.path).pipe(
						Effect.tap((state) =>
							toast.show(
								"success",
								state.port
									? `Dev server restarted at localhost:${state.port}`
									: `Dev server restarting for ${task.id}...`,
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
				})

			/**
			 * Attach to the dev server tmux session for the currently selected bead
			 *
			 * Switches the tmux client to the dev server session, allowing the user
			 * to view the dev server output. User can return with Ctrl-a Ctrl-a.
			 */
			const attachDevServer = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getSelectedTask()
					if (!task) {
						yield* toast.show("error", "No task selected")
						return
					}

					const currentState = yield* devServer.getStatus(task.id)

					if (currentState.status !== "running" && currentState.status !== "starting") {
						yield* toast.show("error", "No dev server running. Start one with Space+r")
						return
					}

					if (!currentState.tmuxSession) {
						yield* toast.show("error", "Dev server session not found")
						return
					}

					// Switch to the dev server session
					yield* tmux
						.switchClient(currentState.tmuxSession)
						.pipe(
							Effect.catchAll((err) =>
								toast.show(
									"error",
									`Failed to attach: ${err instanceof Error ? err.message : String(err)}`,
								),
							),
						)
				})

			return {
				toggleDevServer,
				restartDevServer,
				attachDevServer,
			}
		}),
	},
) {}
