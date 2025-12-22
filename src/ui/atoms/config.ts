/**
 * Config atoms
 */

import { Effect } from "effect"
import { AppConfig } from "../../config/index.js"
import { appRuntime } from "./runtime.js"

/**
 * Atom for the application configuration
 */
export const appConfigAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const appConfig = yield* AppConfig
		return appConfig.config
	}),
)
