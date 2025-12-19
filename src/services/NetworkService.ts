/**
 * NetworkService - monitors network connectivity for offline mode
 *
 * Periodically checks if github.com (or configured host) is reachable.
 * Other services can subscribe to isOnline to enable/disable network operations.
 *
 * When network is unavailable:
 * - Git push/fetch operations are silently skipped
 * - PR creation is disabled with a message
 * - Beads sync is skipped
 *
 * Config options control behavior:
 * - network.autoDetect: Enable/disable automatic detection (default: true)
 * - network.checkIntervalSeconds: How often to check (default: 30)
 * - network.checkHost: Host to check (default: "github.com")
 */

import { Duration, Effect, Schedule, SubscriptionRef } from "effect"
import { AppConfig } from "../config/AppConfig.js"
import { DiagnosticsService } from "./DiagnosticsService.js"

/**
 * Check connectivity by attempting HTTP HEAD to the configured host
 *
 * Uses a 5-second timeout to avoid blocking on slow networks.
 * Returns true if reachable, false otherwise.
 */
const checkConnectivity = (host: string): Effect.Effect<boolean> =>
	Effect.tryPromise({
		try: () =>
			fetch(`https://${host}`, {
				method: "HEAD",
				signal: AbortSignal.timeout(5000),
			}),
		catch: () => new Error("Network unreachable"),
	}).pipe(
		Effect.map(() => true),
		Effect.orElse(() => Effect.succeed(false)),
	)

export class NetworkService extends Effect.Service<NetworkService>()("NetworkService", {
	dependencies: [DiagnosticsService.Default, AppConfig.Default],
	scoped: Effect.gen(function* () {
		const diagnostics = yield* DiagnosticsService
		const appConfig = yield* AppConfig

		const networkConfig = appConfig.getNetworkConfig()

		yield* diagnostics.trackService(
			"NetworkService",
			`Connectivity monitor (${networkConfig.autoDetect ? `checking ${networkConfig.checkHost} every ${networkConfig.checkIntervalSeconds}s` : "disabled"})`,
		)

		// Current network status - starts optimistically online
		const isOnline = yield* SubscriptionRef.make(true)

		// Only start polling if autoDetect is enabled
		if (networkConfig.autoDetect) {
			// Check immediately on startup
			const initialStatus = yield* checkConnectivity(networkConfig.checkHost)
			yield* SubscriptionRef.set(isOnline, initialStatus)

			// Then poll at the configured interval
			yield* Effect.scheduleForked(
				Schedule.spaced(Duration.seconds(networkConfig.checkIntervalSeconds)),
			)(
				Effect.flatMap(checkConnectivity(networkConfig.checkHost), (status) =>
					SubscriptionRef.set(isOnline, status),
				),
			)
		}

		return {
			/**
			 * Current network status - subscribe for reactive updates
			 */
			isOnline,

			/**
			 * Get current network status synchronously
			 *
			 * Use this for one-off checks, not reactive updates.
			 */
			getIsOnline: (): Effect.Effect<boolean> => SubscriptionRef.get(isOnline),

			/**
			 * Force a connectivity check right now
			 *
			 * Useful when the user wants to retry after coming online.
			 */
			checkNow: (): Effect.Effect<boolean> =>
				Effect.gen(function* () {
					const status = yield* checkConnectivity(networkConfig.checkHost)
					yield* SubscriptionRef.set(isOnline, status)
					return status
				}),
		}
	}),
}) {}
