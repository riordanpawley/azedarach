/**
 * Network status atoms for offline mode
 *
 * Provides reactive access to network connectivity status for UI components.
 */

import { Effect } from "effect"
import { NetworkService } from "../../services/NetworkService.js"
import { appRuntime } from "./runtime.js"

/**
 * Atom that subscribes to network online status
 *
 * Returns true when online, false when offline.
 */
export const isOnlineAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const networkService = yield* NetworkService
		return networkService.isOnline
	}),
)
