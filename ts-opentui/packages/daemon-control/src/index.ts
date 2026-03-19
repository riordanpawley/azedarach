import {
	classifyDaemonRpcClientError,
	type DaemonRpcClientApi,
	type DaemonRpcClientError,
	isDaemonRpcClientRetryableTransport,
} from "@azedarach/shared/rpc"
import type { RpcClientError } from "@effect/rpc/RpcClientError"
import { Context, Data } from "effect"

export interface GlobalDaemonDiscoveryMetadata {
	readonly schemaVersion: 1
	readonly pid: number
	readonly lockId: string
	readonly socketPath: string
	readonly startedAtMs: number
}

export class GlobalDaemonBootstrapError extends Data.TaggedError("GlobalDaemonBootstrapError")<{
	readonly message: string
	readonly reason:
		| "discovery-read"
		| "discovery-clear"
		| "discovery-timeout"
		| "spawn-failed"
		| "rpc-protocol-mismatch"
		| "rpc-remote-action"
		| "rpc-transport"
		| "rpc-timeout"
		| "rpc-unknown"
		| "endpoint-timeout"
}> {}

export interface GlobalDaemonAttachAttemptObservation {
	readonly attempt: number
	readonly delayMs: number
	readonly timeoutRemainingMs: number
	readonly socketPath: string | null
	readonly socketUrl: string | null
}

export interface GlobalDaemonBootstrapApi {
	readonly bootstrapDaemonRpcClient: (params?: {
		readonly autoStart: boolean
		readonly timeoutMs?: number
		readonly attachRetryBackoffMs?: ReadonlyArray<number>
		readonly onAttachAttempt?: (observation: GlobalDaemonAttachAttemptObservation) => void
		readonly verifyReachable?: boolean
	}) => PromiseOrEffect<
		{
			readonly client: DaemonRpcClientApi
			readonly discovery: GlobalDaemonDiscoveryMetadata
			readonly socketUrl: string
			readonly startedDaemon: boolean
			readonly attachAttemptCount: number
		},
		GlobalDaemonBootstrapError
	>
	readonly formatDaemonRpcClientFailure: (params: {
		readonly operation: string
		readonly socketUrl: string
		readonly error: DaemonRpcClientError
	}) => GlobalDaemonBootstrapError
	readonly isRetryableRpcClientError: (error: DaemonRpcClientError) => error is RpcClientError
	readonly buildGlobalDaemonSocketUrl: (socketPath: string) => string
}

type PromiseOrEffect<A, E> = import("effect").Effect.Effect<A, E>

export class GlobalDaemonBootstrap extends Context.Tag("GlobalDaemonBootstrap")<
	GlobalDaemonBootstrap,
	GlobalDaemonBootstrapApi
>() {}

export const buildGlobalDaemonSocketUrl = (socketPath: string): string =>
	`ws+unix://${socketPath}:/`

export const isRetryableRpcClientError = (error: DaemonRpcClientError): error is RpcClientError =>
	isDaemonRpcClientRetryableTransport(error)

export const formatDaemonRpcClientFailure = (params: {
	readonly operation: string
	readonly socketUrl: string
	readonly error: DaemonRpcClientError
}): GlobalDaemonBootstrapError => {
	switch (classifyDaemonRpcClientError(params.error)) {
		case "remote-action": {
			if (params.error._tag !== "DaemonRpcActionError") {
				return new GlobalDaemonBootstrapError({
					message: `Daemon RPC '${params.operation}' failed due to an unexpected daemon response shape.`,
					reason: "rpc-unknown",
				})
			}
			const actionHint = params.error.action === undefined ? "" : ` Action: ${params.error.action}.`
			return new GlobalDaemonBootstrapError({
				message: `Daemon RPC '${params.operation}' rejected by daemon (code=${params.error.code}): ${params.error.message}.${actionHint}`,
				reason: "rpc-remote-action",
			})
		}
		case "protocol-mismatch":
			return new GlobalDaemonBootstrapError({
				message: `Daemon RPC protocol mismatch for '${params.operation}' at ${params.socketUrl}: ${params.error.message}. Update the CLI/daemon to matching versions, then run \`az daemon restart\`.`,
				reason: "rpc-protocol-mismatch",
			})
		case "transport":
			return new GlobalDaemonBootstrapError({
				message: `Unable to communicate with daemon RPC endpoint (${params.socketUrl}) for '${params.operation}': ${params.error.message}. Verify daemon socket availability, then run \`az daemon health\` and \`az daemon logs\`.`,
				reason: "rpc-transport",
			})
	}
}
