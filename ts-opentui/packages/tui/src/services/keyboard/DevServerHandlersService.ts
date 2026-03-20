import { AppConfig } from "@azedarach/config"
import { Effect } from "effect"
import { OverlayService } from "../OverlayService.js"
import { ToastService } from "../ToastService.js"
import { TuiDevServerService } from "../TuiDevServerService.js"
import { TuiProjectContextService } from "../TuiProjectContextService.js"
import { KeyboardHelpersService } from "./KeyboardHelpersService.js"

export interface DevServerHandlersServiceApi {
	readonly toggleDevServer: () => Effect.Effect<void>
	readonly restartDevServer: () => Effect.Effect<void>
}

export class DevServerHandlersService extends Effect.Service<DevServerHandlersService>()(
	"DevServerHandlersService",
	{
		dependencies: [
			TuiDevServerService.Default,
			TuiProjectContextService.Default,
			ToastService.Default,
			KeyboardHelpersService.Default,
			OverlayService.Default,
			AppConfig.Default,
		],
		effect: Effect.gen(function* () {
			const devServer = yield* TuiDevServerService
			const projectContext = yield* TuiProjectContextService
			const toast = yield* ToastService
			const helpers = yield* KeyboardHelpersService
			const overlay = yield* OverlayService
			const appConfig = yield* AppConfig

			const resolveServerNames = () =>
				appConfig
					.getDevServerConfig()
					.pipe(
						Effect.map((config) =>
							config?.servers !== undefined && Object.keys(config.servers).length > 0
								? Object.keys(config.servers)
								: ["default"],
						),
					)

			const toggleDevServer = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (task === undefined) {
						yield* toast.show("error", "No task selected")
						return
					}

					const project = yield* projectContext
						.requireCurrentProject()
						.pipe(
							Effect.catchTag("TuiProjectContextError", (error) =>
								toast.show("error", error.message).pipe(Effect.as(undefined)),
							),
						)
					if (project === undefined) {
						return
					}

					const serverNames = yield* resolveServerNames()
					if (serverNames.length > 1) {
						yield* overlay.push({ _tag: "devServerMenu", issueId: task.id })
						return
					}

					const serverName = serverNames[0] ?? "default"
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
						Effect.catchTag("TuiDevServerServiceError", (error) =>
							toast.show("error", error.message),
						),
					)
				})

			const restartDevServer = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (task === undefined) {
						yield* toast.show("error", "No task selected")
						return
					}

					const project = yield* projectContext
						.requireCurrentProject()
						.pipe(
							Effect.catchTag("TuiProjectContextError", (error) =>
								toast.show("error", error.message).pipe(Effect.as(undefined)),
							),
						)
					if (project === undefined) {
						return
					}

					const serverNames = yield* resolveServerNames()
					if (serverNames.length > 1) {
						yield* overlay.push({ _tag: "devServerMenu", issueId: task.id })
						return
					}

					const serverName = serverNames[0] ?? "default"
					const state = yield* devServer
						.getStatus(task.id, serverName, project.path)
						.pipe(
							Effect.catchTag("TuiDevServerServiceError", (error) =>
								toast.show("error", error.message).pipe(Effect.as(undefined)),
							),
						)
					if (state === undefined) {
						return
					}

					if (state.status !== "running" && state.status !== "starting") {
						yield* toast.show("error", `Dev server '${serverName}' not running to restart`)
						return
					}

					yield* toast.show("info", `Restarting dev server '${serverName}'...`)
					yield* devServer
						.stop(task.id, serverName, project.path)
						.pipe(
							Effect.catchTag("TuiDevServerServiceError", (error) =>
								toast.show("error", error.message).pipe(Effect.as(undefined)),
							),
						)
					yield* devServer.start(task.id, project.path, serverName).pipe(
						Effect.tap((nextState) =>
							toast.show(
								"success",
								nextState.status === "running"
									? `Dev server '${serverName}' restarted at localhost:${nextState.port}`
									: `Dev server '${serverName}' restarting...`,
							),
						),
						Effect.catchTag("TuiDevServerServiceError", (error) =>
							toast.show("error", error.message),
						),
					)
				})

			return {
				toggleDevServer,
				restartDevServer,
			} satisfies DevServerHandlersServiceApi
		}),
	},
) {}
