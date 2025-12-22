/**
 * DevServerHandlersService - Keyboard handlers for dev server actions
 *
 * Provides action handlers for dev server operations:
 * - Toggle dev server (Space+r)
 * - Restart dev server (Space+Ctrl+r)
 * - Attach to dev server (Space+v)
 */

import { Effect, HashMap } from "effect"
import { AppConfig } from "../../config/AppConfig.js"
import { TmuxService } from "../../core/TmuxService.js"
import { DevServerService, type NoWorktreeError } from "../DevServerService.js"
import { OverlayService } from "../OverlayService.js"
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
			OverlayService.Default,
			AppConfig.Default,
		],
		effect: Effect.gen(function* () {
			const devServer = yield* DevServerService
			const projectService = yield* ProjectService
			const toast = yield* ToastService
			const helpers = yield* KeyboardHelpersService
			const tmux = yield* TmuxService
			const overlay = yield* OverlayService
			const appConfig = yield* AppConfig

			/**
			 * Toggle dev server for the currently selected bead
			 *
			 * If multiple servers are defined, shows a menu.
			 * Otherwise toggles the default server.
			 */
			const toggleDevServer = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
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

					const config = yield* appConfig.getDevServerConfig()
					const serverNames = config?.servers ? Object.keys(config.servers) : ["default"]

					if (serverNames.length > 1) {
						// Show menu for multiple servers
						yield* overlay.push({ _tag: "devServerMenu", beadId: task.id })
					} else {
						// Toggle the single/default server
						const serverName = serverNames[0]
						const currentState = yield* devServer.getStatus(task.id, serverName)

						if (currentState.status === "running" || currentState.status === "starting") {
							// Stop the server
							yield* devServer.stop(task.id, serverName)
							yield* toast.show("success", `Stopped dev server '${serverName}' for ${task.id}`)
						} else {
							// Start the server
							yield* devServer.toggle(task.id, project.path, serverName).pipe(
								Effect.tap((state) =>
									toast.show(
										"success",
										state.port
											? `Dev server '${serverName}' running at localhost:${state.port}`
											: `Dev server '${serverName}' starting for ${task.id}...`,
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
					}
				})

			/**
			 * Restart dev server for the currently selected bead
			 */
			const restartDevServer = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
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

					const config = yield* appConfig.getDevServerConfig()
					const serverNames = config?.servers ? Object.keys(config.servers) : ["default"]

					if (serverNames.length > 1) {
						// For now, if multiple servers, just show the menu (Space+r is better for picking)
						yield* overlay.push({ _tag: "devServerMenu", beadId: task.id })
					} else {
						const serverName = serverNames[0]
						const currentState = yield* devServer.getStatus(task.id, serverName)

						if (currentState.status !== "running" && currentState.status !== "starting") {
							yield* toast.show("error", `Dev server '${serverName}' not running to restart`)
							return
						}

						// Stop then start
						yield* toast.show("info", `Restarting dev server '${serverName}' for ${task.id}...`)
						yield* devServer.stop(task.id, serverName)

						yield* devServer.toggle(task.id, project.path, serverName).pipe(
							Effect.tap((state) =>
								toast.show(
									"success",
									state.port
										? `Dev server '${serverName}' restarted at localhost:${state.port}`
										: `Dev server '${serverName}' restarting for ${task.id}...`,
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
			 * Attach to a dev server tmux session
			 */
			const attachDevServer = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) {
						yield* toast.show("error", "No task selected")
						return
					}

					const beadServers = yield* devServer.getBeadServers(task.id)
					const runningServers = HashMap.filter(
						beadServers,
						(s) => s.status === "running" || s.status === "starting",
					)

					if (HashMap.size(runningServers) === 0) {
						yield* toast.show("error", "No dev server running. Start one with Space+r")
						return
					}

					if (HashMap.size(runningServers) > 1) {
						// Multiple running, show menu to pick which one to attach to
						yield* overlay.push({ _tag: "devServerMenu", beadId: task.id })
					} else {
						// Only one running, attach to it
						const server = HashMap.values(runningServers).next().value
						if (!server?.tmuxSession) {
							yield* toast.show("error", "Dev server session not found")
							return
						}

						yield* tmux
							.switchClient(server.tmuxSession)
							.pipe(
								Effect.catchAll((err) =>
									toast.show(
										"error",
										`Failed to attach: ${err instanceof Error ? err.message : String(err)}`,
									),
								),
							)
					}
				})

			return {
				toggleDevServer,
				restartDevServer,
				attachDevServer,
			}
		}),
	},
) {}
