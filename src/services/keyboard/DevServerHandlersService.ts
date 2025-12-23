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
						yield* overlay.push({ _tag: "devServerMenu", beadId: task.id, mode: "toggle" })
					} else {
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
							Effect.catchAll((err) => {
								const message =
									err._tag === "NoWorktreeError" ||
									err._tag === "WorktreeSessionError" ||
									err._tag === "TmuxError" ||
									err._tag === "SessionNotFoundError"
										? err.message
										: String(err)
								return toast.show("error", message)
							}),
						)
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
						yield* overlay.push({ _tag: "devServerMenu", beadId: task.id, mode: "toggle" })
					} else {
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
							Effect.catchAll((err) => {
								const message =
									err._tag === "NoWorktreeError" ||
									err._tag === "WorktreeSessionError" ||
									err._tag === "TmuxError" ||
									err._tag === "SessionNotFoundError"
										? err.message
										: String(err)
								return toast.show("error", message)
							}),
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
					const runningCount = HashMap.size(
						HashMap.filter(beadServers, (s) => s.status === "running" || s.status === "starting"),
					)

					if (runningCount === 0) {
						yield* toast.show("error", "No dev server running. Start one with Space+r")
						return
					}

					if (runningCount > 1) {
						yield* overlay.push({ _tag: "devServerMenu", beadId: task.id, mode: "attach" })
					} else {
						const server = Array.from(HashMap.values(beadServers)).find(
							(s) => s.status === "running" || s.status === "starting",
						)
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
