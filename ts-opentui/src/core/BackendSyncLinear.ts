import { Effect } from "effect"
import type { BackendSyncInterface } from "./BackendSyncInterface.js"
import { IssueSyncService } from "./IssueSyncService.js"

export class BackendSyncLinear extends Effect.Service<BackendSyncLinear>()("BackendSyncLinear", {
	dependencies: [IssueSyncService.Default],
	effect: Effect.gen(function* () {
		const issueSyncService = yield* IssueSyncService

		return {
			target: "linear",
			bootstrap: (projectPath: string) => issueSyncService.bootstrapLinear(projectPath),
			flushQueue: (projectPath: string, options?) =>
				issueSyncService.flushLinearQueue(projectPath, options),
		} satisfies BackendSyncInterface
	}),
}) {}
