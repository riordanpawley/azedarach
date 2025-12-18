/**
 * DiagnosticsService - Tracks system health and long-running fibers
 *
 * Provides a central place to monitor:
 * - Long-running fibers (polling loops, watchers) and their status
 * - Service health (HookReceiver, PTYMonitor, etc.)
 * - Session states
 * - Recent activity
 *
 * Fibers register themselves using acquireRelease pattern so their
 * status automatically updates when they're interrupted/complete.
 */

import { Effect, Fiber, FiberId, SubscriptionRef } from "effect"

// ============================================================================
// Types
// ============================================================================

/**
 * Status of a registered fiber
 */
export type FiberStatus = "running" | "completed" | "interrupted" | "failed"

/**
 * Registered fiber info
 */
export interface RegisteredFiber {
	readonly id: string
	readonly name: string
	readonly description: string
	readonly startedAt: Date
	readonly status: FiberStatus
	readonly fiberId: string
	readonly endedAt?: Date
	readonly error?: string
}

/**
 * Service health info
 */
export interface ServiceHealth {
	readonly name: string
	readonly status: "healthy" | "degraded" | "unhealthy"
	readonly lastActivity?: Date
	readonly details?: string
}

/**
 * Full diagnostics state
 */
export interface DiagnosticsState {
	readonly fibers: readonly RegisteredFiber[]
	readonly services: readonly ServiceHealth[]
	readonly lastUpdated: Date
}

// ============================================================================
// Service Implementation
// ============================================================================

/**
 * DiagnosticsService
 *
 * Provides fiber registration with automatic status tracking via finalizers.
 *
 * @example
 * ```ts
 * // Register a fiber with automatic cleanup
 * yield* diagnostics.registerFiber({
 *   id: "hook-receiver-poller",
 *   name: "HookReceiver Poller",
 *   description: "Polls /tmp for notification files",
 *   fiber: pollerFiber,
 * })
 * ```
 */
export class DiagnosticsService extends Effect.Service<DiagnosticsService>()("DiagnosticsService", {
	scoped: Effect.gen(function* () {
		const stateRef = yield* SubscriptionRef.make<DiagnosticsState>({
			fibers: [],
			services: [],
			lastUpdated: new Date(),
		})

		/**
		 * Register a fiber for monitoring
		 *
		 * Uses Effect.ensuring to automatically update status when fiber completes.
		 * Returns an effect that should be used to wrap the fiber's execution.
		 */
		const registerFiber = <A, E>(options: {
			id: string
			name: string
			description: string
			fiber: Fiber.RuntimeFiber<A, E>
		}) =>
			Effect.gen(function* () {
				const { id, name, description, fiber } = options
				const fiberId = FiberId.threadName(fiber.id())

				// Add fiber to state as running
				const entry: RegisteredFiber = {
					id,
					name,
					description,
					startedAt: new Date(),
					status: "running",
					fiberId,
				}

				yield* SubscriptionRef.update(stateRef, (s) => ({
					...s,
					fibers: [...s.fibers.filter((f) => f.id !== id), entry],
					lastUpdated: new Date(),
				}))

				// Set up finalizer to update status when fiber completes
				// Use forkScoped so the watcher survives the trackFiber call
				yield* Effect.forkScoped(
					Fiber.await(fiber).pipe(
						Effect.flatMap((exit) =>
							SubscriptionRef.update(stateRef, (s) => {
								const status: FiberStatus = exit._tag === "Success" ? "completed" : "failed"
								const error =
									exit._tag === "Failure"
										? `${exit.cause._tag}: ${JSON.stringify(exit.cause)}`
										: undefined

								return {
									...s,
									fibers: s.fibers.map((f) =>
										f.id === id
											? {
													...f,
													status,
													endedAt: new Date(),
													error,
												}
											: f,
									),
									lastUpdated: new Date(),
								}
							}),
						),
					),
				)

				return fiber
			})

		/**
		 * Update a service's health status
		 */
		const updateServiceHealth = (health: ServiceHealth) =>
			SubscriptionRef.update(stateRef, (s) => ({
				...s,
				services: [...s.services.filter((svc) => svc.name !== health.name), health],
				lastUpdated: new Date(),
			}))

		/**
		 * Record activity for a service (updates lastActivity timestamp)
		 */
		const recordActivity = (serviceName: string, details?: string) =>
			SubscriptionRef.update(stateRef, (s) => ({
				...s,
				services: s.services.map((svc) =>
					svc.name === serviceName
						? { ...svc, lastActivity: new Date(), details: details ?? svc.details }
						: svc,
				),
				lastUpdated: new Date(),
			}))

		/**
		 * Get current diagnostics snapshot
		 */
		const getSnapshot = () => SubscriptionRef.get(stateRef)

		/**
		 * Clear completed/failed fibers from display
		 */
		const clearCompletedFibers = () =>
			SubscriptionRef.update(stateRef, (s) => ({
				...s,
				fibers: s.fibers.filter((f) => f.status === "running"),
				lastUpdated: new Date(),
			}))

		/**
		 * Track a scoped service's lifecycle using acquireRelease
		 *
		 * Call this in a scoped service constructor. It will:
		 * - Mark the service as "healthy" when acquired
		 * - Mark the service as "unhealthy" when the scope closes
		 *
		 * @example
		 * ```ts
		 * // In a scoped service:
		 * export class MyService extends Effect.Service<MyService>()("MyService", {
		 *   scoped: Effect.gen(function* () {
		 *     const diagnostics = yield* DiagnosticsService
		 *     yield* diagnostics.trackService("MyService", "Doing important work")
		 *     // ... rest of service setup
		 *   }),
		 * }) {}
		 * ```
		 */
		const trackService = (name: string, details: string) =>
			Effect.acquireRelease(updateServiceHealth({ name, status: "healthy", details }), () =>
				updateServiceHealth({
					name,
					status: "unhealthy",
					details: "Service stopped",
				}),
			)

		return {
			state: stateRef,
			registerFiber,
			updateServiceHealth,
			recordActivity,
			getSnapshot,
			clearCompletedFibers,
			trackService,
		}
	}),
}) {}
