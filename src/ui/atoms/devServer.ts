import { Atom, Result } from "@effect-atom/atom"
import { Effect, HashMap, Option } from "effect"
import {
	type BeadDevServersState,
	DevServerService,
	type DevServerState,
} from "../../services/DevServerService.js"
import { NavigationService } from "../../services/NavigationService.js"
import { ProjectService } from "../../services/ProjectService.js"
import { appRuntime } from "./runtime.js"

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

export const focusedBeadDevServersAtom = Atom.readable((get) => {
	const serversResult = get(devServersAtom)
	if (!Result.isSuccess(serversResult)) return HashMap.empty<string, DevServerState>()

	const focusedIdResult = get(focusedTaskIdAtom)
	if (!Result.isSuccess(focusedIdResult) || !focusedIdResult.value)
		return HashMap.empty<string, DevServerState>()

	const beadId = focusedIdResult.value
	return HashMap.get(serversResult.value, beadId).pipe(
		Option.getOrElse(() => HashMap.empty<string, DevServerState>()),
	)
})

const IDLE_SERVER: DevServerState = {
	status: "idle" as const,
	port: undefined,
	name: "default",
	tmuxSession: undefined,
	worktreePath: undefined,
	startedAt: undefined,
	error: undefined,
}

export const focusedBeadPrimaryDevServerAtom = Atom.readable((get) => {
	const beadServers = get(focusedBeadDevServersAtom)

	const running = HashMap.filter(beadServers, (s) => s.status === "running")
	const runningValues = Array.from(HashMap.values(running))
	if (runningValues.length > 0) {
		return runningValues[0]
	}

	return HashMap.get(beadServers, "default").pipe(Option.getOrElse(() => IDLE_SERVER))
})

export const beadDevServersAtom = (beadId: string) =>
	Atom.readable((get) => {
		const serversResult = get(devServersAtom)
		if (!Result.isSuccess(serversResult)) {
			return HashMap.empty<string, DevServerState>()
		}

		return HashMap.get(serversResult.value, beadId).pipe(
			Option.getOrElse(() => HashMap.empty<string, DevServerState>()),
		)
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

export const devServerStateAtom = (beadId: string, serverName: string) =>
	Atom.readable((get) => {
		const beadServers = get(beadDevServersAtom(beadId))
		return HashMap.get(beadServers, serverName).pipe(Option.getOrElse(() => IDLE_SERVER))
	})
