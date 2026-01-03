/**
 * @since 1.0.0
 */
import * as RateLimiter from "@effect/experimental/RateLimiter";
import * as Effect from "effect/Effect";
import * as Activity from "./Activity.js";
import * as DurableClock from "./DurableClock.js";
/**
 * @since 1.0.0
 * @category Accessors
 */
export const rateLimit = options => Activity.make({
  name: `DurableRateLimiter/${options.name}`,
  error: RateLimiter.RateLimitStoreError,
  execute: Effect.gen(function* () {
    const limiter = yield* RateLimiter.RateLimiter;
    const result = yield* limiter.consume({
      onExceeded: "delay",
      ...options
    }).pipe(Effect.catchIf(e => e.reason === "Exceeded", Effect.die));
    return yield* DurableClock.sleep({
      name: `DurableRateLimiter/${options.name}`,
      duration: result.delay
    });
  })
});
//# sourceMappingURL=DurableRateLimiter.js.map