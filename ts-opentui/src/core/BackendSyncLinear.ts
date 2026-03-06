import { Effect } from "effect"
import type { BackendSyncInterface } from "./BackendSyncInterface.js"
import { IssueSyncService } from "./IssueSyncService.js"

export class BackendSyncLinear extends Effect.Service<BackendSyncLinear>()("BackendSyncLinear", {
	dependencies: [IssueSyncService.Default],
	effect: Effect.gen(function* () {
		const issueSyncService = yield* IssueSyncService

		return {
			target: "linear",
			bootstrap: (cwd?: string) => issueSyncService.bootstrapLinear(cwd),
			flushQueue: (cwd?: string) => issueSyncService.flushLinearQueue(cwd),
		} satisfies BackendSyncInterface
	}),
}) {}
