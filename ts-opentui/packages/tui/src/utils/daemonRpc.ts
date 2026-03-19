import { DaemonRpcClient, type DaemonRpcClientError } from "@azedarach/shared/rpc"
import { Effect, Option } from "effect"
import type { ImplementationRegistry } from "../contracts.js"

const fallbackImplementationRegistry = (): ImplementationRegistry => ({
	default_implementation: "default",
	implicit_default_allowed: true,
	implementations: [
		{
			name: "default",
			description: undefined,
			directory: undefined,
			created_at: "1970-01-01T00:00:00.000Z",
			updated_at: "1970-01-01T00:00:00.000Z",
			is_default: true,
			is_builtin: true,
		},
	],
})

export const getImplementationRegistryFromDaemon = (
	projectPath?: string,
): Effect.Effect<ImplementationRegistry, DaemonRpcClientError> =>
	Effect.gen(function* () {
		const maybeDaemonRpcClient = yield* Effect.serviceOption(DaemonRpcClient)
		if (Option.isNone(maybeDaemonRpcClient)) {
			return fallbackImplementationRegistry()
		}
		const daemonRpcClient = maybeDaemonRpcClient.value
		const response = yield* daemonRpcClient.implementationGetRegistry(
			projectPath === undefined ? undefined : { projectPath },
		)
		return response.registry
	})
