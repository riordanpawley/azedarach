/**
 * OfflineService - centralized offline mode decision making
 *
 * Combines configuration settings with network status to determine
 * if specific operations should be enabled or disabled.
 *
 * Logic:
 * - If config explicitly disables a feature (e.g., git.pushEnabled: false),
 *   it stays disabled regardless of network status
 * - If network is offline (detected by NetworkService), operations are disabled
 * - Both conditions must be true for an operation to be enabled
 *
 * Usage:
 * ```typescript
 * const offline = yield* OfflineService
 * if (yield* offline.isGitPushEnabled()) {
 *   yield* runGit("push", ...)
 * }
 * ```
 */

import { Effect, SubscriptionRef } from "effect"
import { AppConfig } from "../config/AppConfig.js"
import { DiagnosticsService } from "./DiagnosticsService.js"
import { NetworkService } from "./NetworkService.js"

/**
 * Reason why an operation is disabled
 */
export type DisabledReason = "config" | "offline" | "both"

/**
 * Result of checking if an operation is enabled
 */
export type EnabledStatus =
	| { readonly enabled: true }
	| { readonly enabled: false; readonly reason: DisabledReason }

export class OfflineService extends Effect.Service<OfflineService>()("OfflineService", {
	dependencies: [DiagnosticsService.Default, AppConfig.Default, NetworkService.Default],
	scoped: Effect.gen(function* () {
		const diagnostics = yield* DiagnosticsService
		const appConfig = yield* AppConfig
		const network = yield* NetworkService

		yield* diagnostics.trackService("OfflineService", "Offline mode decision service")

		const gitConfig = appConfig.getGitConfig()
		const prConfig = appConfig.getPRConfig()
		const beadsConfig = appConfig.getBeadsConfig()

		/**
		 * Helper to check if operation is enabled based on config + network
		 */
		const checkEnabled = (configEnabled: boolean): Effect.Effect<EnabledStatus> =>
			Effect.gen(function* () {
				const online = yield* SubscriptionRef.get(network.isOnline)

				if (!configEnabled && !online) {
					return { enabled: false, reason: "both" } satisfies EnabledStatus
				}
				if (!configEnabled) {
					return { enabled: false, reason: "config" } satisfies EnabledStatus
				}
				if (!online) {
					return { enabled: false, reason: "offline" } satisfies EnabledStatus
				}
				return { enabled: true } satisfies EnabledStatus
			})

		return {
			/**
			 * Check if git push operations are enabled
			 *
			 * Disabled if:
			 * - git.pushEnabled is false in config
			 * - Network is offline
			 */
			isGitPushEnabled: (): Effect.Effect<EnabledStatus> => checkEnabled(gitConfig.pushEnabled),

			/**
			 * Check if git fetch/pull operations are enabled
			 *
			 * Disabled if:
			 * - git.fetchEnabled is false in config
			 * - Network is offline
			 */
			isGitFetchEnabled: (): Effect.Effect<EnabledStatus> => checkEnabled(gitConfig.fetchEnabled),

			/**
			 * Check if PR creation is enabled
			 *
			 * Disabled if:
			 * - pr.enabled is false in config
			 * - Network is offline
			 */
			isPREnabled: (): Effect.Effect<EnabledStatus> => checkEnabled(prConfig.enabled),

			/**
			 * Check if beads sync is enabled
			 *
			 * Disabled if:
			 * - beads.syncEnabled is false in config
			 * - Network is offline
			 */
			isBeadsSyncEnabled: (): Effect.Effect<EnabledStatus> => checkEnabled(beadsConfig.syncEnabled),

			/**
			 * Get descriptive message for why an operation is disabled
			 */
			getDisabledMessage: (operation: string, status: EnabledStatus): string => {
				if (status.enabled) return ""
				switch (status.reason) {
					case "config":
						return `${operation} disabled in config`
					case "offline":
						return `${operation} unavailable (offline)`
					case "both":
						return `${operation} disabled (config + offline)`
				}
			},
		}
	}),
}) {}
