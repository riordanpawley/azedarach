import { Data, Effect } from "effect"
import type { DaemonRpcClientError } from "./DaemonRpcClient.js"

export class DaemonRpcMethodUnavailableError extends Data.TaggedError(
	"DaemonRpcMethodUnavailableError",
)<{
	readonly methodName: string
	readonly message: string
}> {}

export const daemonRpcMethodUnavailableError = (
	methodName: string,
): DaemonRpcMethodUnavailableError =>
	new DaemonRpcMethodUnavailableError({
		methodName,
		message: `Daemon RPC method is unavailable: ${methodName}`,
	})

export const mapDaemonRpcClientErrorMessage = <E>(
	error: DaemonRpcClientError,
	createError: (message: string) => E,
): E => createError(error.message)

export const invokeOptionalDaemonRpcMethod = <Request, Result, Error>({
	method,
	methodName,
	request,
	onUnavailable,
	onError,
}: {
	readonly method: ((request: Request) => Effect.Effect<Result, DaemonRpcClientError>) | undefined
	readonly methodName: string
	readonly request: Request
	readonly onUnavailable: (methodName: string) => Error
	readonly onError: (error: DaemonRpcClientError) => Error
}): Effect.Effect<Result, Error> =>
	method === undefined
		? Effect.fail(onUnavailable(methodName))
		: method(request).pipe(Effect.mapError(onError))
