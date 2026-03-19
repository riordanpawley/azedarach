import { Data, Effect } from "effect"

export interface BackendFlushQueueOptions {
	readonly hydrateRemote?: boolean
}

export class BackendSyncRuntimeError extends Data.TaggedError("BackendSyncRuntimeError")<{
	readonly message: string
}> {}

export interface BackendSyncRuntime {
	readonly flushQueue: (
		projectPath: string,
		options?: BackendFlushQueueOptions,
	) => Effect.Effect<{ readonly pushed: number; readonly pulled: number }, BackendSyncRuntimeError>
}

export interface BackendSyncResolverApi {
	readonly resolve: () => Effect.Effect<BackendSyncRuntime | undefined>
}

export class BackendSyncResolver extends Effect.Service<BackendSyncResolver>()(
	"BackendSyncResolver",
	{
		effect: Effect.succeed({
			resolve: () => Effect.succeed(undefined),
		} satisfies BackendSyncResolverApi),
	},
) {}
