import type { Effect } from "effect"
import type { IssueSyncError } from "./IssueSyncService.js"

export interface BackendSyncInterface {
	readonly target: "linear"
	readonly bootstrap: (cwd?: string) => Effect.Effect<number, IssueSyncError>
	readonly flushQueue: (
		cwd?: string,
	) => Effect.Effect<{ readonly pushed: number; readonly pulled: number }, IssueSyncError>
}
