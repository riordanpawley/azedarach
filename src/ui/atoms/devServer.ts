import { Atom, Result } from "@effect-atom/atom"
import { Effect, HashMap, Option } from "effect"
import { TmuxService } from "../../core/TmuxService.js"
import {
	DevServerService,
	type DevServerState,
	type DevServerStatus,
} from "../../services/DevServerService.js"
import { NavigationService } from "../../services/NavigationService.js"
import { ProjectService } from "../../services/ProjectService.js"
import { ToastService } from "../../services/ToastService.js"
import { appConfigAtom } from "./config.js"
import { appRuntime } from "./runtime.js"

export interface DevServerView {
	readonly name: string
	readonly status: DevServerStatus
	readonly port?: number
	readonly paneId?: string
	readonly isConfigured: boolean
	readonly tmuxSession?: string
	readonly error?: string
}

export const devServersAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const svc = yield* DevServerService
		return svc.servers
	}),
)

export const focusedTaskIdAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const nav = yield* NavigationService
		return nav.focusedTaskId
	}),
)

export const beadDevServerViewsAtom = (beadId: string) =>
	Atom.readable((get) => {
		const serversResult = get(devServersAtom)
		const configResult = get(appConfigAtom)

		if (!Result.isSuccess(serversResult) || !Result.isSuccess(configResult)) {
			return [] as DevServerView[]
		}

		const runningServers = HashMap.get(serversResult.value, beadId).pipe(
			Option.getOrElse(() => HashMap.empty<string, DevServerState>()),
		)

		const config = configResult.value
		const devServerConfig = config.devServer
		const configuredServers = devServerConfig?.servers ?? {}

		const views: DevServerView[] = []
		const processedNames = new Set<string>()

		for (const [name, _cfg] of Object.entries(configuredServers)) {
			const running = HashMap.get(runningServers, name)
			processedNames.add(name)

			if (Option.isSome(running)) {
				views.push({
					name,
					status: running.value.status,
					port: running.value.port,
					paneId: running.value.paneId,
					isConfigured: true,
					tmuxSession: running.value.tmuxSession,
					error: running.value.error,
				})
			} else {
				views.push({
					name,
					status: "idle",
					isConfigured: true,
				})
			}
		}

		for (const [name, state] of HashMap.entries(runningServers)) {
			if (!processedNames.has(name)) {
				views.push({
					name,
					status: state.status,
					port: state.port,
					paneId: state.paneId,
					isConfigured: false,
					tmuxSession: state.tmuxSession,
					error: state.error,
				})
			}
		}

		return views
	})

export const focusedBeadDevServerViewsAtom = Atom.readable((get) => {
	const focusedIdResult = get(focusedTaskIdAtom)
	if (!Result.isSuccess(focusedIdResult) || !focusedIdResult.value) {
		return [] as DevServerView[]
	}
	return get(beadDevServerViewsAtom(focusedIdResult.value))
})

const IDLE_VIEW: DevServerView = {
	name: "default",
	status: "idle",
	isConfigured: false,
}

export const focusedBeadPrimaryDevServerAtom = Atom.readable((get) => {
	const views = get(focusedBeadDevServerViewsAtom)
	const running = views.find((v) => v.status === "running" || v.status === "starting")
	if (running) return running

	const defaultSrv = views.find((v) => v.name === "default")
	return defaultSrv ?? views[0] ?? IDLE_VIEW
})

export const toggleDevServerAtom = appRuntime.fn((args: { beadId: string; serverName: string }) =>
	Effect.gen(function* () {
		const svc = yield* DevServerService
		const projectService = yield* ProjectService
		const project = yield* projectService.requireCurrentProject()
		const path = project.path

		return yield* svc.toggle(args.beadId, path, args.serverName)
	}),
)

export const attachDevServerAtom = appRuntime.fn((args: { beadId: string; serverName: string }) =>
	Effect.gen(function* () {
		const devServer = yield* DevServerService
		const tmux = yield* TmuxService
		const toast = yield* ToastService

		// Get the server state for the specific server
		const serverState = yield* devServer.getStatus(args.beadId, args.serverName)

		if (serverState.status !== "running" && serverState.status !== "starting") {
			yield* toast.show("error", `Dev server ${args.serverName} is not running`)
			return
		}

		if (!serverState.tmuxSession) {
			yield* toast.show("error", `Dev server session not found for ${args.serverName}`)
			return
		}

		// Attach to the tmux session
		yield* tmux.switchClient(serverState.tmuxSession).pipe(
			Effect.catchAll((err) => {
				if (err._tag === "SessionNotFoundError") {
					return toast.show("error", `Session not found: ${err.session}`)
				}
				if (err._tag === "TmuxError") {
					return toast.show("error", `tmux error: ${err.message}`)
				}
				return toast.show("error", "Failed to attach to dev server session")
			}),
		)
	}),
)

export const stopDevServerAtom = appRuntime.fn((args: { beadId: string; serverName: string }) =>
	Effect.gen(function* () {
		const svc = yield* DevServerService
		return yield* svc.stop(args.beadId, args.serverName)
	}),
)

export const syncDevServerStateAtom = appRuntime.fn((beadId: string) =>
	Effect.gen(function* () {
		const svc = yield* DevServerService
		const servers = yield* svc.getBeadServers(beadId)
		for (const [name] of HashMap.entries(servers)) {
			yield* svc.syncState(beadId, name)
		}
	}),
)
