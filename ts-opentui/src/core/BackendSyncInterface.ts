import type { Effect } from "effect"
import type { IssueSyncError } from "./IssueSyncService.js"

export interface BackendFlushQueueOptions {
	readonly hydrateRemote?: boolean
}

export interface BackendSyncInterface {
	readonly target: "linear"
	readonly bootstrap: (projectPath: string) => Effect.Effect<number, IssueSyncError>
	readonly flushQueue: (
		projectPath: string,
		options?: BackendFlushQueueOptions,
	) => Effect.Effect<{ readonly pushed: number; readonly pulled: number }, IssueSyncError>
}
