/**
 * DevServerHandlersService - Keyboard handlers for dev server actions
 *
 * Provides action handlers for dev server operations:
 * - Toggle dev server (Space+r) - opens unified overlay if multiple servers
 * - Restart dev server (Space+Ctrl+r)
 */

import { Effect } from "effect"
import { AppConfig } from "../../config/AppConfig.js"
import { DevServerService } from "../DevServerService.js"
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
			OverlayService.Default,
			AppConfig.Default,
		],
		effect: Effect.gen(function* () {
			const devServer = yield* DevServerService
			const projectService = yield* ProjectService
			const toast = yield* ToastService
			const helpers = yield* KeyboardHelpersService
			const overlay = yield* OverlayService
			const appConfig = yield* AppConfig

			/**
			 * Toggle dev server for the currently selected bead
			 *
			 * If multiple servers are defined, shows a unified overlay.
			 * Otherwise toggles the default server directly.
			 */
			const toggleDevServer = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) {
						yield* toast.show("error", "No task selected")
						return
					}

					const project = yield* projectService.requireCurrentProject().pipe(
						Effect.catchTag("NoProjectsError", (err) =>
							Effect.gen(function* () {
								yield* toast.show("error", err.message)
								return undefined
							}),
						),
					)
					if (!project) return

					const config = yield* appConfig.getDevServerConfig()
					const serverNames = config?.servers ? Object.keys(config.servers) : ["default"]

					if (serverNames.length > 1) {
						// Multiple servers: show unified overlay
						yield* overlay.push({ _tag: "devServerMenu", beadId: task.id })
					} else {
						// Single server: toggle directly
						const serverName = serverNames[0]
						yield* devServer.toggle(task.id, project.path, serverName).pipe(
							Effect.tap((state) =>
								toast.show(
									"success",
									state.status === "running"
										? `Dev server '${serverName}' running at localhost:${state.port}`
										: state.status === "starting"
											? `Dev server '${serverName}' starting...`
											: `Dev server '${serverName}' stopped`,
								),
							),
							Effect.catchTag("NoWorktreeError", (err) => toast.show("error", err.message)),
							Effect.catchTag("DevServerError", (err) => toast.show("error", err.message)),
							Effect.catchTag("TmuxError", (err) =>
								toast.show("error", `tmux error: ${err.message}`),
							),
							Effect.catchTag("SessionNotFoundError", (err) =>
								toast.show("error", `Session not found: ${err.session}`),
							),
							Effect.catchTag("ShellNotReadyError", (err) => toast.show("error", err.message)),
						)
					}
				})

			/**
			 * Restart dev server for the currently selected bead
			 *
			 * If multiple servers, shows unified overlay.
			 * Otherwise restarts the default server directly.
			 */
			const restartDevServer = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) {
						yield* toast.show("error", "No task selected")
						return
					}

					const project = yield* projectService.requireCurrentProject().pipe(
						Effect.catchTag("NoProjectsError", (err) =>
							Effect.gen(function* () {
								yield* toast.show("error", err.message)
								return undefined
							}),
						),
					)
					if (!project) return

					const config = yield* appConfig.getDevServerConfig()
					const serverNames = config?.servers ? Object.keys(config.servers) : ["default"]

					if (serverNames.length > 1) {
						// Multiple servers: show unified overlay (user can toggle from there)
						yield* overlay.push({ _tag: "devServerMenu", beadId: task.id })
					} else {
						// Single server: restart directly
						const serverName = serverNames[0]
						const state = yield* devServer.getStatus(task.id, serverName)

						if (state.status !== "running" && state.status !== "starting") {
							yield* toast.show("error", `Dev server '${serverName}' not running to restart`)
							return
						}

						yield* toast.show("info", `Restarting dev server '${serverName}'...`)
						yield* devServer.stop(task.id, serverName)
						yield* devServer.start(task.id, project.path, serverName).pipe(
							Effect.tap((s) =>
								toast.show(
									"success",
									s.status === "running"
										? `Dev server '${serverName}' restarted at localhost:${s.port}`
										: `Dev server '${serverName}' restarting...`,
								),
							),
							Effect.catchTag("NoWorktreeError", (err) => toast.show("error", err.message)),
							Effect.catchTag("DevServerError", (err) => toast.show("error", err.message)),
							Effect.catchTag("TmuxError", (err) =>
								toast.show("error", `tmux error: ${err.message}`),
							),
							Effect.catchTag("SessionNotFoundError", (err) =>
								toast.show("error", `Session not found: ${err.session}`),
							),
							Effect.catchTag("ShellNotReadyError", (err) => toast.show("error", err.message)),
						)
					}
				})

			return {
				toggleDevServer,
				restartDevServer,
			}
		}),
	},
) {}
