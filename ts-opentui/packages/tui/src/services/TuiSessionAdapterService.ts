import { resolveProjectPathFromContext } from "@azedarach/shared/project-path"
import {
	DaemonRpcClient,
	type DaemonSessionMutationResult,
	type DaemonSessionSnapshotEntry,
} from "@azedarach/shared/rpc"
import { Data, Effect } from "effect"
import { TuiProjectContextService } from "./TuiProjectContextService.js"

export class TuiSessionAdapterServiceError extends Data.TaggedError(
	"TuiSessionAdapterServiceError",
)<{
	readonly reason: "rpc-failed"
	readonly operation:
		| "sessionListActive"
		| "sessionStart"
		| "sessionStop"
		| "sessionPause"
		| "sessionResume"
		| "sessionRecover"
	readonly message: string
}> {}

export interface TuiSessionAdapterServiceApi {
	readonly listActive: (options?: {
		readonly projectPath?: string
	}) => Effect.Effect<ReadonlyArray<DaemonSessionSnapshotEntry>, TuiSessionAdapterServiceError>
	readonly start: (
		issueId: string,
		options?: {
			readonly projectPath?: string
			readonly initialPrompt?: string
			readonly imagePaths?: readonly string[]
			readonly sessionEnv?: Readonly<Record<string, string>>
			readonly dangerouslySkipPermissions?: boolean
		},
	) => Effect.Effect<DaemonSessionSnapshotEntry, TuiSessionAdapterServiceError>
	readonly stop: (
		issueId: string,
		options?: {
			readonly projectPath?: string
		},
	) => Effect.Effect<DaemonSessionSnapshotEntry, TuiSessionAdapterServiceError>
	readonly pause: (
		issueId: string,
		options?: {
			readonly projectPath?: string
		},
	) => Effect.Effect<DaemonSessionSnapshotEntry, TuiSessionAdapterServiceError>
	readonly resume: (
		issueId: string,
		options?: {
			readonly projectPath?: string
		},
	) => Effect.Effect<DaemonSessionSnapshotEntry, TuiSessionAdapterServiceError>
	readonly recover: (
		issueId: string,
		options?: {
			readonly projectPath?: string
		},
	) => Effect.Effect<DaemonSessionSnapshotEntry, TuiSessionAdapterServiceError>
	readonly recoverSession: (
		issueId: string,
		options?: {
			readonly projectPath?: string
		},
	) => Effect.Effect<DaemonSessionSnapshotEntry, TuiSessionAdapterServiceError>
}

const rpcFailure = (
	operation: TuiSessionAdapterServiceError["operation"],
	message: string,
): TuiSessionAdapterServiceError =>
	new TuiSessionAdapterServiceError({
		reason: "rpc-failed",
		operation,
		message,
	})

const toSession = (result: DaemonSessionMutationResult): DaemonSessionSnapshotEntry =>
	result.session

export class TuiSessionAdapterService extends Effect.Service<TuiSessionAdapterService>()(
	"TuiSessionAdapterService",
	{
		dependencies: [TuiProjectContextService.Default],
		effect: Effect.gen(function* () {
			const daemonRpcClient = yield* DaemonRpcClient
			const projectContext = yield* TuiProjectContextService

			const resolveProjectPath = (projectPath: string | undefined): Effect.Effect<string> =>
				Effect.gen(function* () {
					if (projectPath !== undefined) {
						return projectPath
					}
					return yield* resolveProjectPathFromContext(projectContext)
				})

			const service: TuiSessionAdapterServiceApi = {
				listActive: (options) => {
					if (daemonRpcClient.sessionSnapshot === undefined) {
						return Effect.fail(
							rpcFailure("sessionListActive", "Daemon RPC client unavailable for session snapshot"),
						)
					}

					return resolveProjectPath(options?.projectPath).pipe(
						Effect.flatMap((projectPath) =>
							daemonRpcClient.sessionSnapshot({
								projectPath,
							}),
						),
						Effect.map((result) => result.sessions),
						Effect.mapError((error) => rpcFailure("sessionListActive", error.message)),
					)
				},
				start: (issueId, options) => {
					if (daemonRpcClient.sessionStart === undefined) {
						return Effect.fail(
							rpcFailure("sessionStart", "Daemon RPC client unavailable for session start"),
						)
					}

					return resolveProjectPath(options?.projectPath).pipe(
						Effect.flatMap((projectPath) =>
							daemonRpcClient.sessionStart({
								issueId,
								projectPath,
								...(options?.initialPrompt !== undefined
									? { initialPrompt: options.initialPrompt }
									: {}),
								...(options?.imagePaths !== undefined ? { imagePaths: options.imagePaths } : {}),
								...(options?.sessionEnv !== undefined ? { sessionEnv: options.sessionEnv } : {}),
								...(options?.dangerouslySkipPermissions !== undefined
									? { dangerouslySkipPermissions: options.dangerouslySkipPermissions }
									: {}),
							}),
						),
						Effect.map(toSession),
						Effect.mapError((error) => rpcFailure("sessionStart", error.message)),
					)
				},
				stop: (issueId, options) => {
					if (daemonRpcClient.sessionStop === undefined) {
						return Effect.fail(
							rpcFailure("sessionStop", "Daemon RPC client unavailable for session stop"),
						)
					}

					return resolveProjectPath(options?.projectPath).pipe(
						Effect.flatMap((projectPath) =>
							daemonRpcClient.sessionStop({
								issueId,
								projectPath,
							}),
						),
						Effect.map(toSession),
						Effect.mapError((error) => rpcFailure("sessionStop", error.message)),
					)
				},
				pause: (issueId, options) => {
					if (daemonRpcClient.sessionPause === undefined) {
						return Effect.fail(
							rpcFailure("sessionPause", "Daemon RPC client unavailable for session pause"),
						)
					}

					return resolveProjectPath(options?.projectPath).pipe(
						Effect.flatMap((projectPath) =>
							daemonRpcClient.sessionPause({
								issueId,
								projectPath,
							}),
						),
						Effect.map(toSession),
						Effect.mapError((error) => rpcFailure("sessionPause", error.message)),
					)
				},
				resume: (issueId, options) => {
					if (daemonRpcClient.sessionResume === undefined) {
						return Effect.fail(
							rpcFailure("sessionResume", "Daemon RPC client unavailable for session resume"),
						)
					}

					return resolveProjectPath(options?.projectPath).pipe(
						Effect.flatMap((projectPath) =>
							daemonRpcClient.sessionResume({
								issueId,
								projectPath,
							}),
						),
						Effect.map(toSession),
						Effect.mapError((error) => rpcFailure("sessionResume", error.message)),
					)
				},
				recover: (issueId, options) => {
					if (daemonRpcClient.sessionRecover === undefined) {
						return Effect.fail(
							rpcFailure("sessionRecover", "Daemon RPC client unavailable for session recover"),
						)
					}

					return resolveProjectPath(options?.projectPath).pipe(
						Effect.flatMap((projectPath) =>
							daemonRpcClient.sessionRecover({
								issueId,
								projectPath,
							}),
						),
						Effect.map(toSession),
						Effect.mapError((error) => rpcFailure("sessionRecover", error.message)),
					)
				},
				recoverSession: (issueId, options) => service.recover(issueId, options),
			}

			return service
		}),
	},
) {}
