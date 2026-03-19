export { runGlobalDaemonMain } from "../../packages/daemon/src/GlobalDaemonMain.js"

import { BunContext } from "@effect/platform-bun"
import { Effect } from "effect"
import { GlobalDaemonDiscovery } from "../../packages/daemon/src/GlobalDaemonDiscovery.js"
import { runGlobalDaemonMain } from "../../packages/daemon/src/GlobalDaemonMain.js"

if (import.meta.main) {
	Effect.runPromise(
		runGlobalDaemonMain.pipe(
			Effect.provide(GlobalDaemonDiscovery.Default),
			Effect.provide(BunContext.layer),
		),
	).catch(() => {
		process.exitCode = 1
	})
}
