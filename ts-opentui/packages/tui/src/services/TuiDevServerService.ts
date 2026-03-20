import {
	type DaemonDevServerMutationResult,
	type DaemonDevServerState,
	type DaemonDevServerStatusResult,
	DaemonRpcClient,
	type DaemonRpcClientApi,
} from "@azedarach/shared/rpc"
import { Data, Effect } from "effect"
import type { DevServerState } from "../contracts.js"

export class TuiDevServerServiceError extends Data.TaggedError("TuiDevServerServiceError")<{
	readonly reason: "rpc-unavailable" | "rpc-failed"
	readonly operation: "status" | "start" | "stop" | "toggle"
	readonly message: string
}> {}

export interface TuiDevServerServiceApi {
	readonly getStatus: (
		issueId: string,
		serverName: string,
		projectPath: string,
	) => Effect.Effect<DevServerState, TuiDevServerServiceError>
	readonly start: (
		issueId: string,
		projectPath: string,
		serverName: string,
	) => Effect.Effect<DevServerState, TuiDevServerServiceError>
	readonly stop: (
		issueId: string,
		serverName: string,
		projectPath: string,
	) => Effect.Effect<DevServerState, TuiDevServerServiceError>
	readonly toggle: (
		issueId: string,
		projectPath: string,
		serverName: string,
	) => Effect.Effect<DevServerState, TuiDevServerServiceError>
}

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

const rpcUnavailable = (
	operation: TuiDevServerServiceError["operation"],
	message: string,
): TuiDevServerServiceError =>
	new TuiDevServerServiceError({
		reason: "rpc-unavailable",
		operation,
		message,
	})

const rpcFailed = (
	operation: TuiDevServerServiceError["operation"],
	message: string,
): TuiDevServerServiceError =>
	new TuiDevServerServiceError({
		reason: "rpc-failed",
		operation,
		message,
	})

const callStatus = (
	daemonRpcClient: DaemonRpcClientApi,
	options: { readonly issueId: string; readonly serverName: string; readonly projectPath: string },
): Effect.Effect<DaemonDevServerStatusResult, TuiDevServerServiceError> => {
	const status = daemonRpcClient.devServerStatus
	if (status === undefined) {
		return Effect.fail(rpcUnavailable("status", "daemon RPC dev-server status unavailable"))
	}

	return status(options).pipe(Effect.mapError((error) => rpcFailed("status", error.message)))
}

const callStart = (
	daemonRpcClient: DaemonRpcClientApi,
	options: { readonly issueId: string; readonly serverName: string; readonly projectPath: string },
): Effect.Effect<DaemonDevServerMutationResult, TuiDevServerServiceError> => {
	const start = daemonRpcClient.devServerStart
	if (start === undefined) {
		return Effect.fail(rpcUnavailable("start", "daemon RPC dev-server start unavailable"))
	}

	return start(options).pipe(Effect.mapError((error) => rpcFailed("start", error.message)))
}

const callStop = (
	daemonRpcClient: DaemonRpcClientApi,
	options: { readonly issueId: string; readonly serverName: string; readonly projectPath: string },
): Effect.Effect<DaemonDevServerMutationResult, TuiDevServerServiceError> => {
	const stop = daemonRpcClient.devServerStop
	if (stop === undefined) {
		return Effect.fail(rpcUnavailable("stop", "daemon RPC dev-server stop unavailable"))
	}

	return stop(options).pipe(Effect.mapError((error) => rpcFailed("stop", error.message)))
}

export class TuiDevServerService extends Effect.Service<TuiDevServerService>()(
	"TuiDevServerService",
	{
		effect: Effect.gen(function* () {
			const daemonRpcClient = yield* DaemonRpcClient

			return {
				getStatus: (issueId, serverName, projectPath) =>
					callStatus(daemonRpcClient, {
						issueId,
						serverName,
						projectPath,
					}).pipe(Effect.map((result) => toDevServerState(result.server))),
				start: (issueId, projectPath, serverName) =>
					callStart(daemonRpcClient, {
						issueId,
						serverName,
						projectPath,
					}).pipe(Effect.map((result) => toDevServerState(result.server))),
				stop: (issueId, serverName, projectPath) =>
					callStop(daemonRpcClient, {
						issueId,
						serverName,
						projectPath,
					}).pipe(Effect.map((result) => toDevServerState(result.server))),
				toggle: (issueId, projectPath, serverName) =>
					callStatus(daemonRpcClient, {
						issueId,
						serverName,
						projectPath,
					}).pipe(
						Effect.flatMap((current) =>
							current.server.status === "running" || current.server.status === "starting"
								? callStop(daemonRpcClient, {
										issueId,
										serverName,
										projectPath,
									})
								: callStart(daemonRpcClient, {
										issueId,
										serverName,
										projectPath,
									}),
						),
						Effect.map((result) => toDevServerState(result.server)),
						Effect.mapError((error) =>
							error.operation === "status"
								? new TuiDevServerServiceError({
										reason: error.reason,
										operation: "toggle",
										message: error.message,
									})
								: error,
						),
					),
			} satisfies TuiDevServerServiceApi
		}),
	},
) {}
