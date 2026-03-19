import {
	type DaemonDevServerListRequest,
	type DaemonDevServerListResult,
	type DaemonDevServerMutationResult,
	type DaemonDevServerStartRequest,
	type DaemonDevServerState,
	type DaemonDevServerStatusRequest,
	type DaemonDevServerStatusResult,
	type DaemonDevServerStopRequest,
	DaemonRpcClient,
	type DaemonRpcClientApi,
} from "@azedarach/shared/rpc"
import { Atom, Result } from "@effect-atom/atom"
import { Data, Effect, HashMap, Option, Schedule, SubscriptionRef } from "effect"
import type { DevServerStatus } from "../contracts.js"
import {
	NavigationService,
	ProjectService,
	TmuxService,
	ToastService,
} from "../utils/runtimeServices.js"
import { appConfigAtom } from "./config.js"
import { appRuntime } from "./runtime.js"

export interface DevServerView {
	readonly name: string
	readonly status: DevServerStatus
	readonly port?: number
	readonly windowName?: string
	readonly isConfigured: boolean
	readonly tmuxSession?: string
	readonly error?: string
}

interface DevServerState {
	readonly name: string
	readonly status: DevServerStatus
	readonly port: number | undefined
	readonly windowName: string | undefined
	readonly tmuxSession: string | undefined
	readonly worktreePath: string | undefined
	readonly startedAt: Date | undefined
	readonly error: string | undefined
}

type IssueDevServersState = HashMap.HashMap<string, DevServerState>
type DevServersState = HashMap.HashMap<string, IssueDevServersState>

const EMPTY_DEV_SERVER_VIEWS: readonly DevServerView[] = []
const EMPTY_DEV_SERVERS_STATE: DevServersState = HashMap.empty()
const DEFAULT_SERVER_NAME = "default"

class TuiDevServerRpcUnavailableError extends Data.TaggedError("TuiDevServerRpcUnavailableError")<{
	readonly message: string
}> {}

const parseStartedAt = (startedAt: string | null): Date | undefined => {
	if (startedAt === null) return undefined
	const parsed = new Date(startedAt)
	return Number.isNaN(parsed.getTime()) ? undefined : parsed
}

const toDevServerState = (server: DaemonDevServerState): DevServerState => ({
	name: server.serverName,
	status: server.status,
	port: server.port ?? undefined,
	windowName: server.windowName ?? undefined,
	tmuxSession: server.tmuxSession ?? undefined,
	worktreePath: server.worktreePath ?? undefined,
	startedAt: parseStartedAt(server.startedAt),
	error: server.error ?? undefined,
})

const toIssueDevServersState = (
	servers: ReadonlyArray<DaemonDevServerState>,
): IssueDevServersState => {
	let issueServers: IssueDevServersState = HashMap.empty()
	for (const server of servers) {
		issueServers = HashMap.set(issueServers, server.serverName, toDevServerState(server))
	}
	return issueServers
}

const toDevServersState = (servers: ReadonlyArray<DaemonDevServerState>): DevServersState => {
	let allServers: DevServersState = HashMap.empty()
	for (const server of servers) {
		const issueServers = HashMap.get(allServers, server.issueId).pipe(
			Option.getOrElse(() => HashMap.empty<string, DevServerState>()),
		)
		allServers = HashMap.set(
			allServers,
			server.issueId,
			HashMap.set(issueServers, server.serverName, toDevServerState(server)),
		)
	}
	return allServers
}

const updateServerInRef = (
	ref: SubscriptionRef.SubscriptionRef<DevServersState>,
	server: DaemonDevServerState,
): Effect.Effect<void> =>
	SubscriptionRef.update(ref, (allServers) => {
		const issueServers = HashMap.get(allServers, server.issueId).pipe(
			Option.getOrElse(() => HashMap.empty<string, DevServerState>()),
		)
		return HashMap.set(
			allServers,
			server.issueId,
			HashMap.set(issueServers, server.serverName, toDevServerState(server)),
		)
	})

const getProjectPath = Effect.gen(function* () {
	const projectService = yield* ProjectService
	return (yield* projectService.getCurrentPath()) ?? process.cwd()
})

const getDaemonRpcClient = (): Effect.Effect<DaemonRpcClientApi, TuiDevServerRpcUnavailableError> =>
	Effect.gen(function* () {
		const daemonRpcClient = yield* Effect.serviceOption(DaemonRpcClient)
		if (daemonRpcClient._tag === "None") {
			return yield* Effect.fail(
				new TuiDevServerRpcUnavailableError({
					message: "daemon RPC unavailable for TUI dev-server operations",
				}),
			)
		}
		return daemonRpcClient.value
	})

const devServerStatus = (
	daemonClient: DaemonRpcClientApi,
	request: Omit<DaemonDevServerStatusRequest, "rpcProtocolVersion">,
): Effect.Effect<DaemonDevServerStatusResult, TuiDevServerRpcUnavailableError> =>
	Effect.gen(function* () {
		const method = daemonClient.devServerStatus
		if (method === undefined) {
			return yield* Effect.fail(
				new TuiDevServerRpcUnavailableError({
					message: "daemon RPC dev-server status unavailable",
				}),
			)
		}
		return yield* method(request)
	}).pipe(
		Effect.mapError(
			() =>
				new TuiDevServerRpcUnavailableError({
					message: "daemon RPC dev-server status unavailable",
				}),
		),
	)

const devServerList = (
	daemonClient: DaemonRpcClientApi,
	request: Omit<DaemonDevServerListRequest, "rpcProtocolVersion">,
): Effect.Effect<DaemonDevServerListResult, TuiDevServerRpcUnavailableError> =>
	Effect.gen(function* () {
		const method = daemonClient.devServerList
		if (method === undefined) {
			return yield* Effect.fail(
				new TuiDevServerRpcUnavailableError({
					message: "daemon RPC dev-server list unavailable",
				}),
			)
		}
		return yield* method(request)
	}).pipe(
		Effect.mapError(
			() =>
				new TuiDevServerRpcUnavailableError({
					message: "daemon RPC dev-server list unavailable",
				}),
		),
	)

const devServerStart = (
	daemonClient: DaemonRpcClientApi,
	request: Omit<DaemonDevServerStartRequest, "rpcProtocolVersion">,
): Effect.Effect<DaemonDevServerMutationResult, TuiDevServerRpcUnavailableError> =>
	Effect.gen(function* () {
		const method = daemonClient.devServerStart
		if (method === undefined) {
			return yield* Effect.fail(
				new TuiDevServerRpcUnavailableError({
					message: "daemon RPC dev-server start unavailable",
				}),
			)
		}
		return yield* method(request)
	}).pipe(
		Effect.mapError(
			() =>
				new TuiDevServerRpcUnavailableError({
					message: "daemon RPC dev-server start unavailable",
				}),
		),
	)

const devServerStop = (
	daemonClient: DaemonRpcClientApi,
	request: Omit<DaemonDevServerStopRequest, "rpcProtocolVersion">,
): Effect.Effect<DaemonDevServerMutationResult, TuiDevServerRpcUnavailableError> =>
	Effect.gen(function* () {
		const method = daemonClient.devServerStop
		if (method === undefined) {
			return yield* Effect.fail(
				new TuiDevServerRpcUnavailableError({
					message: "daemon RPC dev-server stop unavailable",
				}),
			)
		}
		return yield* method(request)
	}).pipe(
		Effect.mapError(
			() =>
				new TuiDevServerRpcUnavailableError({
					message: "daemon RPC dev-server stop unavailable",
				}),
		),
	)

const refreshAllDevServers = (ref: SubscriptionRef.SubscriptionRef<DevServersState>) =>
	Effect.gen(function* () {
		const daemonClient = yield* getDaemonRpcClient()
		const projectPath = yield* getProjectPath
		const result = yield* devServerList(daemonClient, { projectPath })
		yield* SubscriptionRef.set(ref, toDevServersState(result.servers))
	})

const refreshIssueDevServers = (
	ref: SubscriptionRef.SubscriptionRef<DevServersState>,
	issueId: string,
) =>
	Effect.gen(function* () {
		const daemonClient = yield* getDaemonRpcClient()
		const projectPath = yield* getProjectPath
		const result = yield* devServerList(daemonClient, { issueId, projectPath })
		yield* SubscriptionRef.update(ref, (allServers) =>
			HashMap.set(allServers, issueId, toIssueDevServersState(result.servers)),
		)
	})

const logRefreshFailure = (error: TuiDevServerRpcUnavailableError) =>
	Effect.logWarning(error.message).pipe(Effect.asVoid)

export const devServersRefAtom = appRuntime.atom(
	SubscriptionRef.make<DevServersState>(EMPTY_DEV_SERVERS_STATE),
	{
		initialValue: undefined,
	},
)

export const devServerSyncStarterAtom = appRuntime.fn((_: undefined, get) =>
	Effect.gen(function* () {
		const ref = yield* get.result(devServersRefAtom)
		const refresh = refreshAllDevServers(ref).pipe(Effect.catchAll(logRefreshFailure))
		yield* refresh
		yield* Effect.scheduleForked(Schedule.spaced("5 seconds"))(refresh)
	}),
)

export const devServersAtom = appRuntime.subscriptionRef((get) => get.result(devServersRefAtom))

export const focusedTaskIdAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const nav = yield* NavigationService
		return nav.focusedTaskId
	}),
)

export const issueDevServerViewsAtom = (issueId: string) =>
	Atom.readable((get) => {
		const serversResult = get(devServersAtom)
		const configResult = get(appConfigAtom)

		if (!Result.isSuccess(serversResult) || !Result.isSuccess(configResult)) {
			return EMPTY_DEV_SERVER_VIEWS
		}

		const runningServers = HashMap.get(serversResult.value, issueId).pipe(
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
					windowName: running.value.windowName,
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
					windowName: state.windowName,
					isConfigured: false,
					tmuxSession: state.tmuxSession,
					error: state.error,
				})
			}
		}

		return views
	})

export const focusedIssueDevServerViewsAtom = Atom.readable((get) => {
	const focusedIdResult = get(focusedTaskIdAtom)
	if (!Result.isSuccess(focusedIdResult) || !focusedIdResult.value) {
		return EMPTY_DEV_SERVER_VIEWS
	}
	return get(issueDevServerViewsAtom(focusedIdResult.value))
})

const IDLE_VIEW: DevServerView = {
	name: DEFAULT_SERVER_NAME,
	status: "idle",
	isConfigured: false,
}

export const focusedIssuePrimaryDevServerAtom = Atom.readable((get) => {
	const views = get(focusedIssueDevServerViewsAtom)
	const running = views.find((view) => view.status === "running" || view.status === "starting")
	if (running) return running

	const defaultServer = views.find((view) => view.name === DEFAULT_SERVER_NAME)
	return defaultServer ?? views[0] ?? IDLE_VIEW
})

export const toggleDevServerAtom = appRuntime.fn(
	(args: { issueId: string; serverName: string }, get) =>
		Effect.gen(function* () {
			const ref = yield* get.result(devServersRefAtom)
			const daemonClient = yield* getDaemonRpcClient()
			const projectPath = yield* getProjectPath
			const current = yield* devServerStatus(daemonClient, {
				issueId: args.issueId,
				serverName: args.serverName,
				projectPath,
			})
			yield* updateServerInRef(ref, current.server)

			const next =
				current.server.status === "running" || current.server.status === "starting"
					? yield* devServerStop(daemonClient, {
							issueId: args.issueId,
							serverName: args.serverName,
							projectPath,
						})
					: yield* devServerStart(daemonClient, {
							issueId: args.issueId,
							serverName: args.serverName,
							projectPath,
						})

			yield* updateServerInRef(ref, next.server)
			return toDevServerState(next.server)
		}),
)

export const attachDevServerAtom = appRuntime.fn(
	(args: { issueId: string; serverName: string }, get) =>
		Effect.gen(function* () {
			const ref = yield* get.result(devServersRefAtom)
			const daemonClient = yield* getDaemonRpcClient()
			const projectPath = yield* getProjectPath
			const tmux = yield* TmuxService
			const toast = yield* ToastService
			const serverStatus = yield* devServerStatus(daemonClient, {
				issueId: args.issueId,
				serverName: args.serverName,
				projectPath,
			})
			yield* updateServerInRef(ref, serverStatus.server)
			const serverState = toDevServerState(serverStatus.server)

			if (serverState.status !== "running" && serverState.status !== "starting") {
				yield* toast.show("error", `Dev server ${args.serverName} is not running`)
				return
			}

			if (!serverState.tmuxSession) {
				yield* toast.show("error", `Dev server session not found for ${args.serverName}`)
				return
			}

			yield* tmux.switchClient(serverState.tmuxSession).pipe(
				Effect.catchAll((error) => {
					const logError = Effect.logWarning(error)
					if (error._tag === "SessionNotFoundError") {
						return logError.pipe(
							Effect.zipRight(toast.show("error", `Session not found: ${error.session}`)),
						)
					}
					if (error._tag === "TmuxError") {
						return logError.pipe(
							Effect.zipRight(toast.show("error", `tmux error: ${error.message}`)),
						)
					}
					return logError.pipe(
						Effect.zipRight(toast.show("error", "Failed to attach to dev server session")),
					)
				}),
			)
		}),
)

export const stopDevServerAtom = appRuntime.fn(
	(args: { issueId: string; serverName: string }, get) =>
		Effect.gen(function* () {
			const ref = yield* get.result(devServersRefAtom)
			const daemonClient = yield* getDaemonRpcClient()
			const projectPath = yield* getProjectPath
			const stopped = yield* devServerStop(daemonClient, {
				issueId: args.issueId,
				serverName: args.serverName,
				projectPath,
			})
			yield* updateServerInRef(ref, stopped.server)
			return toDevServerState(stopped.server)
		}),
)

export const syncDevServerStateAtom = appRuntime.fn((issueId: string, get) =>
	Effect.gen(function* () {
		const ref = yield* get.result(devServersRefAtom)
		yield* refreshIssueDevServers(ref, issueId)
	}),
)
