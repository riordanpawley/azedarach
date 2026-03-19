import { Data, Effect } from "effect"

export interface BackendDaemonBoardTaskSnapshot {
	readonly updated_at: string
}

export class BackendDaemonBoardStoreError extends Data.TaggedError("BackendDaemonBoardStoreError")<{
	readonly message: string
}> {}

export interface BackendDaemonBoardStoreApi {
	readonly listBoardTasks: (
		projectPath: string,
	) => Effect.Effect<ReadonlyArray<BackendDaemonBoardTaskSnapshot>, BackendDaemonBoardStoreError>
}

export class BackendDaemonBoardStore extends Effect.Service<BackendDaemonBoardStore>()(
	"BackendDaemonBoardStore",
	{
		effect: Effect.succeed({
			listBoardTasks: () => Effect.succeed([]),
		} satisfies BackendDaemonBoardStoreApi),
	},
) {}
