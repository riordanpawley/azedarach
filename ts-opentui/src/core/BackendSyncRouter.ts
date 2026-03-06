import { Effect } from "effect"
import { AppConfig } from "../config/AppConfig.js"
import type { BackendSyncInterface } from "./BackendSyncInterface.js"
import { BackendSyncLinear } from "./BackendSyncLinear.js"

export class BackendSyncRouter extends Effect.Service<BackendSyncRouter>()("BackendSyncRouter", {
	dependencies: [AppConfig.Default, BackendSyncLinear.Default],
	effect: Effect.gen(function* () {
		const appConfig = yield* AppConfig
		const linearSync = yield* BackendSyncLinear

		const resolve = (): Effect.Effect<BackendSyncInterface | undefined> =>
			appConfig
				.getIssueTrackerSyncConfig()
				.pipe(Effect.map((config) => ("linear" in config.issueTracker ? linearSync : undefined)))

		return {
			resolve,
		}
	}),
}) {}

export interface BackendSyncRouterService {
	readonly resolve: () => Effect.Effect<BackendSyncInterface | undefined>
}
