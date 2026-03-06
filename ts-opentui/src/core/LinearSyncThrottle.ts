import { Chunk, Config, Data, Deferred, Effect, Option, Queue, Stream } from "effect"
import { AppConfig } from "../config/AppConfig.js"

export class LinearSyncThrottleError extends Data.TaggedError("LinearSyncThrottleError")<{
	readonly message: string
}> {}

const DEFAULT_MAX_PER_MINUTE = 10
const DEFAULT_BURST = 10

interface ThrottleTask {
	readonly execute: Effect.Effect<void, never>
}

export interface LinearSyncThrottleService {
	readonly enqueue: <A, E>(params: {
		readonly effect: Effect.Effect<A, E>
		readonly maxWaitMs?: number
	}) => Effect.Effect<Option.Option<A>, E>
}

export class LinearSyncThrottle extends Effect.Service<LinearSyncThrottle>()("LinearSyncThrottle", {
	dependencies: [AppConfig.Default],
	scoped: Effect.gen(function* () {
		const serviceScope = yield* Effect.scope
		const appConfig = yield* AppConfig
		const syncConfig = yield* appConfig.getIssueTrackerSyncConfig()
		const configuredLinear =
			"linear" in syncConfig.issueTracker ? syncConfig.issueTracker.linear : undefined
		const overrideMaxPerMinute = yield* Config.option(
			Config.integer("AZEDARACH_LINEAR_SYNC_MAX_PER_MINUTE"),
		).pipe(Effect.orElseSucceed(() => Option.none<number>()))
		const overrideBurst = yield* Config.option(Config.integer("AZEDARACH_LINEAR_SYNC_BURST")).pipe(
			Effect.orElseSucceed(() => Option.none<number>()),
		)
		const overrideWindowMs = yield* Config.option(
			Config.integer("AZEDARACH_LINEAR_SYNC_WINDOW_MS"),
		).pipe(Effect.orElseSucceed(() => Option.none<number>()))
		const maxPerMinute = Math.max(
			1,
			Math.floor(
				Option.getOrUndefined(overrideMaxPerMinute) ??
					configuredLinear?.syncThrottle.maxPerMinute ??
					DEFAULT_MAX_PER_MINUTE,
			),
		)
		const burst = Math.max(
			0,
			Math.floor(
				Option.getOrUndefined(overrideBurst) ??
					configuredLinear?.syncThrottle.burst ??
					DEFAULT_BURST,
			),
		)
		const windowMs = Math.max(1, Math.floor(Option.getOrUndefined(overrideWindowMs) ?? 60_000))

		const throttleQueue = yield* Queue.unbounded<ThrottleTask>()

		yield* Effect.forkIn(
			Stream.fromQueue(throttleQueue).pipe(
				Stream.throttle({
					cost: Chunk.size,
					units: maxPerMinute,
					duration: windowMs,
					burst,
					strategy: "shape",
				}),
				Stream.mapEffect((task) => task.execute, { concurrency: 1, unordered: false }),
				Stream.runDrain,
			),
			serviceScope,
		)

		const enqueue = <A, E>(params: {
			readonly effect: Effect.Effect<A, E>
			readonly maxWaitMs?: number
		}): Effect.Effect<Option.Option<A>, E> =>
			Effect.gen(function* () {
				const deferred = yield* Deferred.make<A, E>()
				const task: ThrottleTask = {
					execute: params.effect.pipe(
						Effect.flatMap((value) => Deferred.succeed(deferred, value)),
						Effect.catchAllCause((cause) => Deferred.failCause(deferred, cause)),
						Effect.asVoid,
					),
				}
				yield* Queue.offer(throttleQueue, task)

				if (params.maxWaitMs === undefined) {
					const value = yield* Deferred.await(deferred)
					return Option.some(value)
				}

				const maxWaitMs = Math.max(0, Math.floor(params.maxWaitMs))
				if (maxWaitMs === 0) {
					return Option.none<A>()
				}

				return yield* Effect.raceFirst(
					Deferred.await(deferred).pipe(Effect.map((value) => Option.some(value))),
					Effect.sleep(`${maxWaitMs} millis`).pipe(Effect.as(Option.none<A>())),
				)
			})

		return {
			enqueue,
		} satisfies LinearSyncThrottleService
	}),
}) {}
