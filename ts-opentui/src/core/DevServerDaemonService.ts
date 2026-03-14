import { Effect, Ref } from "effect"

export type DevServerDaemonStatus = "idle" | "starting" | "running" | "stopped" | "error"

export interface DevServerDaemonState {
	readonly issueId: string
	readonly serverName: string
	readonly status: DevServerDaemonStatus
	readonly port: number | null
	readonly windowName: string | null
	readonly tmuxSession: string | null
	readonly worktreePath: string | null
	readonly projectPath: string | null
	readonly startedAt: string | null
	readonly error: string | null
}

export interface DevServerDaemonStatusRequest {
	readonly issueId: string
	readonly serverName?: string
	readonly projectPath?: string
}

export interface DevServerDaemonStatusResult {
	readonly capturedAtMs: number
	readonly server: DevServerDaemonState
}

export interface DevServerDaemonListRequest {
	readonly issueId?: string
	readonly projectPath?: string
}

export interface DevServerDaemonListResult {
	readonly capturedAtMs: number
	readonly servers: ReadonlyArray<DevServerDaemonState>
}

export interface DevServerDaemonStartRequest {
	readonly issueId: string
	readonly projectPath: string
	readonly serverName?: string
}

export interface DevServerDaemonStopRequest {
	readonly issueId: string
	readonly serverName?: string
	readonly projectPath?: string
}

export interface DevServerDaemonMutationResult {
	readonly capturedAtMs: number
	readonly server: DevServerDaemonState
}

export interface DevServerDaemonServiceApi {
	readonly status: (
		request: DevServerDaemonStatusRequest,
	) => Effect.Effect<DevServerDaemonStatusResult>
	readonly list: (request?: DevServerDaemonListRequest) => Effect.Effect<DevServerDaemonListResult>
	readonly start: (
		request: DevServerDaemonStartRequest,
	) => Effect.Effect<DevServerDaemonMutationResult>
	readonly stop: (
		request: DevServerDaemonStopRequest,
	) => Effect.Effect<DevServerDaemonMutationResult>
}

interface DevServerDaemonMutableState {
	readonly servers: Map<string, DevServerDaemonState>
	readonly nextPort: number
}

const DEFAULT_SERVER_NAME = "default"
const DEFAULT_PORT_BASE = 4_100

const normalizeServerName = (serverName: string | undefined): string =>
	serverName === undefined || serverName.trim().length === 0 ? DEFAULT_SERVER_NAME : serverName

const toKey = (issueId: string, serverName: string): string => `${issueId}:${serverName}`

const resolveWorktreePath = (projectPath: string, issueId: string): string =>
	`${projectPath}/.worktrees/${issueId}`

const toIdleState = (params: {
	readonly issueId: string
	readonly serverName: string
	readonly projectPath: string | null
}): DevServerDaemonState => ({
	issueId: params.issueId,
	serverName: params.serverName,
	status: "idle",
	port: null,
	windowName: null,
	tmuxSession: null,
	worktreePath: null,
	projectPath: params.projectPath,
	startedAt: null,
	error: null,
})

const sortServers = (
	servers: ReadonlyArray<DevServerDaemonState>,
): ReadonlyArray<DevServerDaemonState> =>
	[...servers].sort((left, right) => {
		if (left.issueId !== right.issueId) {
			return left.issueId.localeCompare(right.issueId)
		}
		return left.serverName.localeCompare(right.serverName)
	})

export const makeDevServerDaemonService = (params?: {
	readonly nowMs?: () => number
	readonly portBase?: number
}): Effect.Effect<DevServerDaemonServiceApi> =>
	Effect.gen(function* () {
		const nowMs = params?.nowMs ?? Date.now
		const stateRef = yield* Ref.make<DevServerDaemonMutableState>({
			servers: new Map(),
			nextPort: params?.portBase ?? DEFAULT_PORT_BASE,
		})

		return {
			status: (request: DevServerDaemonStatusRequest) =>
				Ref.get(stateRef).pipe(
					Effect.map((state) => {
						const capturedAtMs = nowMs()
						const serverName = normalizeServerName(request.serverName)
						const key = toKey(request.issueId, serverName)
						const server =
							state.servers.get(key) ??
							toIdleState({
								issueId: request.issueId,
								serverName,
								projectPath: request.projectPath ?? null,
							})
						return {
							capturedAtMs,
							server,
						} satisfies DevServerDaemonStatusResult
					}),
				),
			list: (request?: DevServerDaemonListRequest) =>
				Ref.get(stateRef).pipe(
					Effect.map((state) => {
						const capturedAtMs = nowMs()
						const servers = sortServers(
							[...state.servers.values()].filter((server) => {
								if (request?.issueId !== undefined && server.issueId !== request.issueId) {
									return false
								}
								if (
									request?.projectPath !== undefined &&
									server.projectPath !== request.projectPath
								) {
									return false
								}
								return true
							}),
						)
						return {
							capturedAtMs,
							servers,
						} satisfies DevServerDaemonListResult
					}),
				),
			start: (request: DevServerDaemonStartRequest) =>
				Ref.modify(stateRef, (state) => {
					const capturedAtMs = nowMs()
					const serverName = normalizeServerName(request.serverName)
					const key = toKey(request.issueId, serverName)
					const existing = state.servers.get(key)
					const shouldAllocatePort = existing?.port === null || existing === undefined
					const allocatedPort = shouldAllocatePort ? state.nextPort : existing.port
					const nextPort = shouldAllocatePort ? state.nextPort + 1 : state.nextPort
					const nextServer: DevServerDaemonState = {
						issueId: request.issueId,
						serverName,
						status: "running",
						port: allocatedPort,
						windowName: `dev-${serverName}`,
						tmuxSession: `az-${request.issueId}`,
						worktreePath: resolveWorktreePath(request.projectPath, request.issueId),
						projectPath: request.projectPath,
						startedAt: new Date(capturedAtMs).toISOString(),
						error: null,
					}
					const nextServers = new Map(state.servers)
					nextServers.set(key, nextServer)
					return [
						{
							capturedAtMs,
							server: nextServer,
						} satisfies DevServerDaemonMutationResult,
						{
							servers: nextServers,
							nextPort,
						} satisfies DevServerDaemonMutableState,
					] as const
				}),
			stop: (request: DevServerDaemonStopRequest) =>
				Ref.modify(stateRef, (state) => {
					const capturedAtMs = nowMs()
					const serverName = normalizeServerName(request.serverName)
					const key = toKey(request.issueId, serverName)
					const existing = state.servers.get(key)
					const nextServer: DevServerDaemonState = {
						issueId: request.issueId,
						serverName,
						status: "stopped",
						port: null,
						windowName: null,
						tmuxSession: null,
						worktreePath: null,
						projectPath: request.projectPath ?? existing?.projectPath ?? null,
						startedAt: null,
						error: null,
					}
					const nextServers = new Map(state.servers)
					nextServers.set(key, nextServer)
					return [
						{
							capturedAtMs,
							server: nextServer,
						} satisfies DevServerDaemonMutationResult,
						{
							servers: nextServers,
							nextPort: state.nextPort,
						} satisfies DevServerDaemonMutableState,
					] as const
				}),
		} satisfies DevServerDaemonServiceApi
	})

export class DevServerDaemonService extends Effect.Service<DevServerDaemonService>()(
	"DevServerDaemonService",
	{
		effect: makeDevServerDaemonService(),
	},
) {}
