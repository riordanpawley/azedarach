import { Data, Effect } from "effect"
import {
	TrackerIssueDaemonService,
	type TrackerIssueDaemonServiceApi,
} from "./TrackerIssueDaemonService.js"

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
		dependencies: [TrackerIssueDaemonService.Default],
		effect: Effect.gen(function* () {
			const trackerIssues: TrackerIssueDaemonServiceApi = yield* TrackerIssueDaemonService
			const runtime: BackendSyncRuntime = {
				flushQueue: (projectPath) =>
					trackerIssues.sync(projectPath).pipe(
						Effect.mapError(
							(error) =>
								new BackendSyncRuntimeError({
									message: error.message,
								}),
						),
					),
			}
			return {
				resolve: () => Effect.succeed(runtime),
			} satisfies BackendSyncResolverApi
		}),
	},
) {}
