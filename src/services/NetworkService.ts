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

import { Duration, Effect, SubscriptionRef } from "effect"
import { AppConfig } from "../config/AppConfig.js"
import { DiagnosticsService } from "./DiagnosticsService.js"

/**
 * Check connectivity by attempting to reach the configured host
 *
 * Strategy:
 * 1. Try HEAD request first (lightweight, no body transfer)
 * 2. If HEAD fails, fallback to GET (some servers block HEAD)
 * 3. Consider online if we get ANY response (even error codes like 403)
 *    because that means the network path works
 *
 * Uses a 10-second timeout to handle slower connections.
 * Returns true if reachable, false otherwise.
 */

/** Result of a single connectivity attempt */
type ConnectivityResult =
	| { readonly online: true }
	| { readonly online: false; readonly error: string }

const checkConnectivity = (host: string): Effect.Effect<boolean> =>
	Effect.gen(function* () {
		const url = `https://${host}`
		const timeout = 10000 // 10 seconds

		// Try HEAD first (lightweight)
		const headResult: ConnectivityResult = yield* Effect.tryPromise({
			try: () =>
				fetch(url, {
					method: "HEAD",
					signal: AbortSignal.timeout(timeout),
				}),
			catch: (e) => e,
		}).pipe(
			Effect.map((): ConnectivityResult => {
				// Any response (even 4xx/5xx) means network is working
				// The server responded, so we're online
				return { online: true }
			}),
			Effect.catchAll(
				(error): Effect.Effect<ConnectivityResult> =>
					Effect.succeed({ online: false, error: String(error) }),
			),
		)

		if (headResult.online) {
			return true
		}

		// HEAD failed - try GET as fallback (some servers block HEAD)
		const getResult: ConnectivityResult = yield* Effect.tryPromise({
			try: () =>
				fetch(url, {
					method: "GET",
					signal: AbortSignal.timeout(timeout),
				}),
			catch: (e) => e,
		}).pipe(
			Effect.map((): ConnectivityResult => {
				// Any response means we're online
				return { online: true }
			}),
			Effect.catchAll(
				(error): Effect.Effect<ConnectivityResult> =>
					Effect.succeed({ online: false, error: String(error) }),
			),
		)

		if (getResult.online) {
			return true
		}

		// Both failed - log for debugging
		yield* Effect.logDebug(
			`Connectivity check failed: HEAD error=${headResult.error}, GET error=${getResult.error}`,
		)
		return false
	})

export class NetworkService extends Effect.Service<NetworkService>()("NetworkService", {
	dependencies: [DiagnosticsService.Default, AppConfig.Default],
	scoped: Effect.gen(function* () {
		const diagnostics = yield* DiagnosticsService
		const appConfig = yield* AppConfig

		// NOTE: Config is fetched fresh on each check to pick up changes from project switching.
		// Do NOT capture config at construction time - it becomes stale.
		const initialConfig = yield* appConfig.getNetworkConfig()

		yield* diagnostics.trackService(
			"NetworkService",
			`Connectivity monitor (${initialConfig.autoDetect ? `checking ${initialConfig.checkHost} every ${initialConfig.checkIntervalSeconds}s` : "disabled"})`,
		)

		// Current network status - starts optimistically online
		const isOnline = yield* SubscriptionRef.make(true)

		// Only start polling if autoDetect was enabled at startup
		// (changing autoDetect requires restart)
		if (initialConfig.autoDetect) {
			// Check immediately on startup
			const initialStatus = yield* checkConnectivity(initialConfig.checkHost)
			yield* SubscriptionRef.set(isOnline, initialStatus)

			// Recursive polling loop that fetches fresh config each iteration
			// This allows checkHost and checkIntervalSeconds to change with project switching
			const pollLoop: Effect.Effect<never> = Effect.gen(function* () {
				const config = yield* appConfig.getNetworkConfig()
				yield* Effect.sleep(Duration.seconds(config.checkIntervalSeconds))
				const status = yield* checkConnectivity(config.checkHost)
				yield* SubscriptionRef.set(isOnline, status)
			}).pipe(Effect.forever)

			yield* Effect.forkScoped(pollLoop)
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
			 * Fetches fresh config to pick up changes from project switching.
			 */
			checkNow: (): Effect.Effect<boolean> =>
				Effect.gen(function* () {
					const config = yield* appConfig.getNetworkConfig()
					const status = yield* checkConnectivity(config.checkHost)
					yield* SubscriptionRef.set(isOnline, status)
					return status
				}),
		}
	}),
}) {}
