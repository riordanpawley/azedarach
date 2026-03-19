import { DateTime, Effect, Schedule, SubscriptionRef } from "effect"

export class ClockService extends Effect.Service<ClockService>()("ClockService", {
	scoped: Effect.gen(function* () {
		const initial = yield* DateTime.now
		const now = yield* SubscriptionRef.make<DateTime.Utc>(initial)

		yield* Effect.scheduleForked(Schedule.spaced("1 second"))(
			Effect.flatMap(DateTime.now, (dt) => SubscriptionRef.set(now, dt)),
		)

		return {
			now,
		}
	}),
}) {}
