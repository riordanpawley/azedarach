/**
 * DiagnosticsService - Tracks system health and long-running fibers
 *
 * Provides a central place to monitor:
 * - Long-running fibers (polling loops, watchers) and their status
 * - Service health (TmuxSessionMonitor, PTYMonitor, etc.)
 * - Session states
 * - Recent activity
 *
 * Fibers register themselves using acquireRelease pattern so their
 * status automatically updates when they're interrupted/complete.
 */

import { Cause, Effect, Fiber, FiberId, Ref, Scope, SubscriptionRef } from "effect"
import type { LinearWebhookMode } from "./LinearWebhookService.js"

// ============================================================================
// Types
// ============================================================================

/**
 * Status of a registered fiber
 */
export type FiberStatus = "running" | "completed" | "interrupted" | "failed"

/**
 * Severity level for diagnostic events
 */
export type DiagnosticSeverity = "info" | "warning" | "error"

/**
 * A diagnostic event for logging issues and notifications
 */
export interface DiagnosticEvent {
	readonly id: string
	readonly timestamp: Date
	readonly severity: DiagnosticSeverity
	readonly source: string
	readonly message: string
	readonly details?: string
}

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

export type IssueDbPerfBackend = "linear"
export type IssueDbPerfOperationKind = "read" | "write"
export type IssueDbPerfLastStatus = "success" | "failure"

export type IssueSyncBackend = "linear" | "none"
export type IssueSyncLastStatus = "idle" | "success" | "failure" | "skipped"
export type IssueSyncRuntimeStatus = "ready" | "unavailable"
export type IssueSyncRuntimeReason =
	| "ready"
	| "backend_not_linear"
	| "sync_disabled"
	| "missing_api_key"
	| "config_error"
export type IssueSyncApiKeySource = "direnv" | "config-provider" | "none" | "unknown"
export type IssueSyncRunOperation = "bootstrap" | "flush"

export interface IssueSyncRuntimeHealth {
	readonly status: IssueSyncRuntimeStatus
	readonly reason: IssueSyncRuntimeReason
	readonly projectPath: string
	readonly configuredTeam?: string
	readonly configuredProject?: string
	readonly apiKeySource: IssueSyncApiKeySource
	readonly updatedAt: Date
}

export interface IssueSyncQueueHealth {
	readonly total: number
	readonly pendingReady: number
	readonly pendingDelayed: number
	readonly processingActive: number
	readonly processingStale: number
	readonly failed: number
	readonly updatedAt: Date
}

export interface IssueSyncRunHealth {
	readonly runId: string
	readonly operation: IssueSyncRunOperation
	readonly status: Exclude<IssueSyncLastStatus, "idle">
	readonly startedAt: Date
	readonly finishedAt: Date
	readonly message: string
	readonly pushed: number
	readonly pulled: number
}

export interface IssueSyncFailure {
	readonly issueId: string
	readonly operation: "bootstrap" | "flush" | "upsert" | "close" | "delete"
	readonly error: string
	readonly attempts: number
	readonly occurredAt: Date
}

export interface IssueSyncHealth {
	readonly backend: IssueSyncBackend
	readonly syncEnabled: boolean
	readonly queueDepth: number
	readonly failedCount: number
	readonly lastSyncedAt?: Date
	readonly lastStatus: IssueSyncLastStatus
	readonly lastMessage: string
	readonly lastFailure?: IssueSyncFailure
	readonly runtime?: IssueSyncRuntimeHealth
	readonly queue?: IssueSyncQueueHealth
	readonly lastRun?: IssueSyncRunHealth
}

const BOOTSTRAP_COMPLETENESS_SKIP_MARKERS = [
	"bootstrap skipped (already complete)",
	"bootstrap skipped (local issues already present)",
]

const hasObservedRemotePull = (health: IssueSyncHealth | undefined): boolean =>
	(health?.lastRun?.pulled ?? 0) > 0

const isBootstrapCompletenessSkip = (health: IssueSyncHealth): boolean => {
	if (health.lastStatus !== "skipped") {
		return false
	}
	if (health.lastRun?.operation !== "bootstrap") {
		return false
	}
	const normalizedMessage = health.lastMessage.trim().toLowerCase()
	return BOOTSTRAP_COMPLETENESS_SKIP_MARKERS.some((marker) => normalizedMessage.includes(marker))
}

const normalizeIssueSyncHealth = (
	previous: IssueSyncHealth | undefined,
	next: IssueSyncHealth,
): IssueSyncHealth => {
	if (next.backend !== "linear" || !next.syncEnabled) {
		return next
	}

	const hasPullEvidence = hasObservedRemotePull(previous) || hasObservedRemotePull(next)
	if (hasPullEvidence || !isBootstrapCompletenessSkip(next)) {
		return next
	}

	const unverifiedMessage = `${next.lastMessage}; remote completeness unverified (no pull observed this session)`
	return {
		...next,
		lastStatus: "failure",
		lastMessage: unverifiedMessage,
		lastRun:
			next.lastRun === undefined
				? undefined
				: {
						...next.lastRun,
						status: "failure",
						message: unverifiedMessage,
					},
	}
}

export type LinearWebhookStrategy =
	| "disabled"
	| "sdk-events"
	| "cli-listener"
	| "cli-fallback-listener"
	| "polling-fallback"

export interface LinearWebhookHealth {
	readonly mode: LinearWebhookMode
	readonly strategy: LinearWebhookStrategy
	readonly healthy: boolean
	readonly message: string
	readonly updatedAt: Date
}

export interface IssueDbPerfStats {
	readonly backend: IssueDbPerfBackend
	readonly operation: string
	readonly kind: IssueDbPerfOperationKind
	readonly count: number
	readonly failureCount: number
	readonly avgMs: number
	readonly p50Ms: number
	readonly p95Ms: number
	readonly maxMs: number
	readonly lastMs: number
	readonly lastStatus: IssueDbPerfLastStatus
	readonly lastAt: Date
}

interface IssueDbPerfAccumulator {
	readonly backend: IssueDbPerfBackend
	readonly operation: string
	readonly kind: IssueDbPerfOperationKind
	readonly count: number
	readonly failureCount: number
	readonly totalDurationMs: number
	readonly sampleDurations: readonly number[]
	readonly maxMs: number
	readonly lastMs: number
	readonly lastStatus: IssueDbPerfLastStatus
	readonly lastAt: Date
}

const ISSUE_DB_PERF_SAMPLE_LIMIT = 200
const ISSUE_DB_PERF_READ_SLOW_THRESHOLD_MS = 300
const ISSUE_DB_PERF_WRITE_SLOW_THRESHOLD_MS = 500

const appendSample = (samples: readonly number[], durationMs: number): readonly number[] => {
	const next = [...samples, durationMs]
	return next.length > ISSUE_DB_PERF_SAMPLE_LIMIT
		? next.slice(next.length - ISSUE_DB_PERF_SAMPLE_LIMIT)
		: next
}

const percentileFromSorted = (sortedValues: readonly number[], percentile: number): number => {
	if (sortedValues.length === 0) return 0
	const rawIndex = Math.ceil(sortedValues.length * percentile) - 1
	const boundedIndex = Math.min(Math.max(rawIndex, 0), sortedValues.length - 1)
	return Math.round(sortedValues[boundedIndex] ?? 0)
}

const toIssueDbPerfStats = (accumulator: IssueDbPerfAccumulator): IssueDbPerfStats => {
	const sortedSamples = [...accumulator.sampleDurations].sort((left, right) => left - right)
	return {
		backend: accumulator.backend,
		operation: accumulator.operation,
		kind: accumulator.kind,
		count: accumulator.count,
		failureCount: accumulator.failureCount,
		avgMs: accumulator.count > 0 ? Math.round(accumulator.totalDurationMs / accumulator.count) : 0,
		p50Ms: percentileFromSorted(sortedSamples, 0.5),
		p95Ms: percentileFromSorted(sortedSamples, 0.95),
		maxMs: Math.round(accumulator.maxMs),
		lastMs: Math.round(accumulator.lastMs),
		lastStatus: accumulator.lastStatus,
		lastAt: accumulator.lastAt,
	}
}

/**
 * Full diagnostics state
 */
export interface DiagnosticsState {
	readonly fibers: readonly RegisteredFiber[]
	readonly services: readonly ServiceHealth[]
	readonly events: readonly DiagnosticEvent[]
	readonly issueDbPerf: readonly IssueDbPerfStats[]
	readonly issueSync?: IssueSyncHealth
	readonly linearWebhook?: LinearWebhookHealth
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
 *   id: "tmux-session-monitor-poller",
 *   name: "TmuxSessionMonitor Poller",
 *   description: "Polls tmux sessions for state changes",
 *   fiber: pollerFiber,
 * })
 * ```
 */
export class DiagnosticsService extends Effect.Service<DiagnosticsService>()("DiagnosticsService", {
	scoped: Effect.gen(function* () {
		const stateRef = yield* SubscriptionRef.make<DiagnosticsState>({
			fibers: [],
			services: [],
			events: [],
			issueDbPerf: [],
			issueSync: undefined,
			linearWebhook: undefined,
			lastUpdated: new Date(),
		})
		const issueDbPerfAccumulators = yield* Ref.make<Map<string, IssueDbPerfAccumulator>>(new Map())

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
								const status: FiberStatus =
									exit._tag === "Success"
										? "completed"
										: Cause.isInterruptedOnly(exit.cause)
											? "interrupted"
											: "failed"
								const error =
									exit._tag === "Failure" && !Cause.isInterruptedOnly(exit.cause)
										? Cause.pretty(exit.cause)
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

		/**
		 * Register a fiber for monitoring, using a specific scope for the watcher fiber
		 *
		 * Use this when registering fibers from service methods (not the constructor).
		 * The scope parameter should be the service's scope, captured via `Effect.scope`.
		 *
		 * @example
		 * ```ts
		 * // In a scoped service:
		 * const serviceScope = yield* Effect.scope
		 *
		 * // In a service method:
		 * const fiber = yield* someEffect.pipe(Effect.forkIn(serviceScope))
		 * yield* diagnostics.registerFiberIn(serviceScope, {
		 *   id: "my-fiber",
		 *   name: "My Fiber",
		 *   description: "Does something useful",
		 *   fiber,
		 * })
		 * ```
		 */
		const registerFiberIn = <A, E>(
			scope: Scope.Scope,
			options: {
				id: string
				name: string
				description: string
				fiber: Fiber.RuntimeFiber<A, E>
			},
		) => Scope.extend(scope)(registerFiber(options))

		/**
		 * Log a diagnostic event
		 *
		 * Events are stored in state and can be viewed in the diagnostics overlay.
		 * Use for warnings, errors, or notable info that should be visible to users.
		 */
		const logEvent = (options: {
			severity: DiagnosticSeverity
			source: string
			message: string
			details?: string
		}) =>
			SubscriptionRef.update(stateRef, (s) => ({
				...s,
				events: [
					...s.events,
					{
						id: crypto.randomUUID(),
						timestamp: new Date(),
						...options,
					},
				],
				lastUpdated: new Date(),
			}))

		/**
		 * Clear all events
		 */
		const clearEvents = () =>
			SubscriptionRef.update(stateRef, (s) => ({
				...s,
				events: [],
				lastUpdated: new Date(),
			}))

		/**
		 * Record issue database command timing
		 *
		 * Maintains aggregate command latency stats and emits slow-call events.
		 */
		const recordIssueDbTiming = (options: {
			backend: IssueDbPerfBackend
			operation: string
			kind: IssueDbPerfOperationKind
			durationMs: number
			success: boolean
		}) =>
			Effect.gen(function* () {
				const key = `${options.backend}:${options.operation}`
				const now = new Date()
				const existing = (yield* Ref.get(issueDbPerfAccumulators)).get(key)
				const nextAccumulator: IssueDbPerfAccumulator = existing
					? {
							...existing,
							kind: options.kind,
							count: existing.count + 1,
							failureCount: existing.failureCount + (options.success ? 0 : 1),
							totalDurationMs: existing.totalDurationMs + options.durationMs,
							sampleDurations: appendSample(existing.sampleDurations, options.durationMs),
							maxMs: Math.max(existing.maxMs, options.durationMs),
							lastMs: options.durationMs,
							lastStatus: options.success ? "success" : "failure",
							lastAt: now,
						}
					: {
							backend: options.backend,
							operation: options.operation,
							kind: options.kind,
							count: 1,
							failureCount: options.success ? 0 : 1,
							totalDurationMs: options.durationMs,
							sampleDurations: [options.durationMs],
							maxMs: options.durationMs,
							lastMs: options.durationMs,
							lastStatus: options.success ? "success" : "failure",
							lastAt: now,
						}

				const nextAccumulators = new Map(yield* Ref.get(issueDbPerfAccumulators))
				nextAccumulators.set(key, nextAccumulator)
				yield* Ref.set(issueDbPerfAccumulators, nextAccumulators)

				const issueDbPerf = Array.from(nextAccumulators.values())
					.map((value) => toIssueDbPerfStats(value))
					.sort((left, right) => right.p95Ms - left.p95Ms)

				yield* SubscriptionRef.update(stateRef, (s) => ({
					...s,
					issueDbPerf,
					lastUpdated: new Date(),
				}))

				const thresholdMs =
					options.kind === "read"
						? ISSUE_DB_PERF_READ_SLOW_THRESHOLD_MS
						: ISSUE_DB_PERF_WRITE_SLOW_THRESHOLD_MS
				if (options.durationMs >= thresholdMs) {
					yield* logEvent({
						severity: "info",
						source: "IssueDbPerf",
						message: `${options.backend} ${options.operation} ${Math.round(options.durationMs)}ms`,
						details: `kind=${options.kind} status=${options.success ? "success" : "failure"} threshold=${thresholdMs}ms`,
					})
				}
			})

		const setIssueSyncHealth = (health: IssueSyncHealth) =>
			SubscriptionRef.update(stateRef, (s) => ({
				...s,
				issueSync: normalizeIssueSyncHealth(s.issueSync, health),
				lastUpdated: new Date(),
			}))

		const setLinearWebhookHealth = (health: LinearWebhookHealth) =>
			SubscriptionRef.update(stateRef, (s) => ({
				...s,
				linearWebhook: health,
				lastUpdated: new Date(),
			}))

		/**
		 * Record a timing event when an operation is slow
		 */
		const logTiming = (options: {
			source: string
			name: string
			durationMs: number
			thresholdMs?: number
			details?: string
		}) =>
			Effect.gen(function* () {
				const threshold = options.thresholdMs ?? 250
				if (options.durationMs < threshold) {
					return
				}
				const message = `${options.name} ${options.durationMs}ms`
				yield* logEvent({
					severity: "info",
					source: options.source,
					message,
					details: options.details,
				})
			})

		/**
		 * Measure an effect and emit a timing event when slow
		 */
		const measure = <A, E, R>(
			options: {
				source: string
				name: string
				thresholdMs?: number
				details?: string
			},
			effect: Effect.Effect<A, E, R>,
		) =>
			Effect.gen(function* () {
				const start = Date.now()
				const result = yield* effect
				const durationMs = Date.now() - start
				yield* logTiming({
					source: options.source,
					name: options.name,
					durationMs,
					thresholdMs: options.thresholdMs,
					details: options.details,
				})
				return result
			})

		return {
			state: stateRef,
			registerFiber,
			registerFiberIn,
			updateServiceHealth,
			recordActivity,
			getSnapshot,
			clearCompletedFibers,
			trackService,
			logEvent,
			clearEvents,
			recordIssueDbTiming,
			setIssueSyncHealth,
			setLinearWebhookHealth,
			logTiming,
			measure,
		}
	}),
}) {}
