"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.makeWithRateLimiter = exports.makeSleep = exports.make = exports.layerStoreMemory = exports.layer = exports.TypeId = exports.RateLimiterStore = exports.RateLimiterError = exports.RateLimiter = exports.RateLimitStoreError = exports.RateLimitExceeded = exports.ErrorTypeId = void 0;
var Context = _interopRequireWildcard(require("effect/Context"));
var Duration = _interopRequireWildcard(require("effect/Duration"));
var Effect = _interopRequireWildcard(require("effect/Effect"));
var _Function = require("effect/Function");
var Layer = _interopRequireWildcard(require("effect/Layer"));
var Schema = _interopRequireWildcard(require("effect/Schema"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 */

/**
 * @since 1.0.0
 * @category Type IDs
 */
const TypeId = exports.TypeId = "~@effect/experimental/RateLimiter";
/**
 * @since 1.0.0
 * @category Tags
 */
const RateLimiter = exports.RateLimiter = /*#__PURE__*/Context.GenericTag(TypeId);
/**
 * @since 1.0.0
 * @category Constructors
 */
const make = exports.make = /*#__PURE__*/Effect.gen(function* () {
  const store = yield* RateLimiterStore;
  return (0, _Function.identity)({
    [TypeId]: TypeId,
    consume(options) {
      const tokens = options.tokens ?? 1;
      const onExceeded = options.onExceeded ?? "fail";
      const algorithm = options.algorithm ?? "fixed-window";
      const window = Duration.decode(options.window);
      const windowMillis = Duration.toMillis(window);
      const refillRate = Duration.unsafeDivide(window, options.limit);
      const refillRateMillis = Duration.toMillis(refillRate);
      if (tokens > options.limit) {
        return onExceeded === "fail" ? Effect.fail(new RateLimitExceeded({
          key: options.key,
          retryAfter: window,
          limit: options.limit,
          remaining: 0
        })) : Effect.succeed({
          delay: window,
          limit: options.limit,
          remaining: 0,
          resetAfter: window
        });
      }
      if (algorithm === "fixed-window") {
        return Effect.flatMap(store.fixedWindow({
          key: options.key,
          tokens,
          refillRate,
          limit: onExceeded === "fail" ? options.limit : undefined
        }), ([count, ttl]) => {
          if (onExceeded === "fail") {
            const remaining = options.limit - count;
            if (remaining < 0) {
              return Effect.fail(new RateLimitExceeded({
                key: options.key,
                retryAfter: Duration.millis(ttl),
                limit: options.limit,
                remaining: 0
              }));
            }
            return Effect.succeed({
              delay: Duration.zero,
              limit: options.limit,
              remaining,
              resetAfter: Duration.millis(ttl)
            });
          }
          const ttlTotal = count * refillRateMillis;
          const elapsed = ttlTotal - ttl;
          const windowNumber = Math.floor((count - 1) / options.limit);
          const remaining = windowNumber * windowMillis - elapsed;
          const delay = remaining <= 0 ? Duration.zero : Duration.millis(remaining);
          return Effect.succeed({
            delay,
            limit: options.limit,
            remaining: options.limit - count,
            resetAfter: Duration.times(window, Math.ceil(ttl / windowMillis))
          });
        });
      }
      return Effect.flatMap(store.tokenBucket({
        key: options.key,
        tokens,
        limit: options.limit,
        refillRate,
        allowOverflow: onExceeded === "delay"
      }), remaining => {
        if (onExceeded === "fail") {
          if (remaining < 0) {
            return Effect.fail(new RateLimitExceeded({
              key: options.key,
              retryAfter: Duration.times(refillRate, -remaining),
              limit: options.limit,
              remaining: 0
            }));
          }
          return Effect.succeed({
            delay: Duration.zero,
            limit: options.limit,
            remaining,
            resetAfter: Duration.times(refillRate, options.limit - remaining)
          });
        }
        if (remaining >= 0) {
          return Effect.succeed({
            delay: Duration.zero,
            limit: options.limit,
            remaining,
            resetAfter: Duration.times(refillRate, options.limit - remaining)
          });
        }
        return Effect.succeed({
          delay: Duration.times(refillRate, -remaining),
          limit: options.limit,
          remaining,
          resetAfter: Duration.times(refillRate, options.limit - remaining)
        });
      });
    }
  });
});
/**
 * @since 1.0.0
 * @category Layers
 */
const layer = exports.layer = /*#__PURE__*/Layer.effect(RateLimiter, make);
/**
 * Access a function that applies rate limiting to an effect.
 *
 * ```ts
 * import { RateLimiter } from "@effect/experimental"
 * import { Effect } from "effect"
 *
 * Effect.gen(function*() {
 *   // Access the `withLimiter` function from the RateLimiter module
 *   const withLimiter = yield* RateLimiter.makeWithRateLimiter
 *
 *   // Apply a rate limiter to an effect
 *   yield* Effect.log("Making a request with rate limiting").pipe(
 *     withLimiter({
 *       key: "some-key",
 *       limit: 10,
 *       onExceeded: "delay",
 *       window: "5 seconds",
 *       algorithm: "fixed-window"
 *     })
 *   )
 * })
 * ```
 *
 * @since 1.0.0
 * @category Accessors
 */
const makeWithRateLimiter = exports.makeWithRateLimiter = /*#__PURE__*/Effect.map(RateLimiter, limiter => options => effect => Effect.flatMap(limiter.consume(options), ({
  delay
}) => {
  if (Duration.isZero(delay)) return effect;
  return Effect.delay(effect, delay);
}));
/**
 * Access a function that sleeps when the rate limit is exceeded.
 *
 * ```ts
 * import { RateLimiter } from "@effect/experimental"
 * import { Effect } from "effect"
 *
 * export default Effect.gen(function*() {
 *   // Access the `sleep` function from the RateLimiter module
 *   const sleep = yield* RateLimiter.makeSleep
 *
 *   // Use the `sleep` function with specific rate limiting parameters.
 *   // This will only sleep if the rate limit has been exceeded.
 *   yield* sleep({
 *     key: "some-key",
 *     limit: 10,
 *     window: "5 seconds",
 *     algorithm: "fixed-window"
 *   })
 * })
 * ```
 *
 * @since 1.0.0
 * @category Accessors
 */
const makeSleep = exports.makeSleep = /*#__PURE__*/Effect.map(RateLimiter, limiter => options => Effect.flatMap(limiter.consume({
  ...options,
  onExceeded: "delay"
}), result => {
  if (Duration.isZero(result.delay)) return Effect.succeed(result);
  return Effect.as(Effect.sleep(result.delay), result);
}));
/**
 * @since 1.0.0
 * @category Errors
 */
const ErrorTypeId = exports.ErrorTypeId = "~@effect/experimental/RateLimiter/RateLimiterError";
/**
 * @since 1.0.0
 * @category Errors
 */
class RateLimitExceeded extends /*#__PURE__*/Schema.TaggedError("@effect/experimental/RateLimiter/RateLimitExceeded")("RateLimiterError", {
  retryAfter: Schema.DurationFromMillis,
  key: Schema.String,
  limit: Schema.Number,
  remaining: Schema.Number
}) {
  /**
   * @since 1.0.0
   */
  [ErrorTypeId] = ErrorTypeId;
  /**
   * @since 1.0.0
   */
  reason = "Exceeded";
  /**
   * @since 1.0.0
   */
  get message() {
    return `Rate limit exceeded`;
  }
}
/**
 * @since 1.0.0
 * @category Errors
 */
exports.RateLimitExceeded = RateLimitExceeded;
class RateLimitStoreError extends /*#__PURE__*/Schema.TaggedError("@effect/experimental/RateLimiter/RateLimitStoreError")("RateLimiterError", {
  message: Schema.String,
  cause: /*#__PURE__*/Schema.optional(Schema.Defect)
}) {
  /**
   * @since 1.0.0
   */
  [ErrorTypeId] = ErrorTypeId;
  /**
   * @since 1.0.0
   */
  reason = "StoreError";
}
/**
 * @since 1.0.0
 * @category Errors
 */
exports.RateLimitStoreError = RateLimitStoreError;
const RateLimiterError = exports.RateLimiterError = /*#__PURE__*/Schema.Union(RateLimitExceeded, RateLimitStoreError);
/**
 * @since 1.0.0
 * @category RateLimiterStore
 */
class RateLimiterStore extends /*#__PURE__*/Context.Tag("@effect/experimental/RateLimiter/RateLimiterStore")() {}
/**
 * @since 1.0.0
 * @category RateLimiterStore
 */
exports.RateLimiterStore = RateLimiterStore;
const layerStoreMemory = exports.layerStoreMemory = /*#__PURE__*/Layer.sync(RateLimiterStore, () => {
  const fixedCounters = new Map();
  const tokenBuckets = new Map();
  return RateLimiterStore.of({
    fixedWindow: options => Effect.clockWith(clock => Effect.sync(() => {
      const refillRateMillis = Duration.toMillis(options.refillRate);
      const now = clock.unsafeCurrentTimeMillis();
      let counter = fixedCounters.get(options.key);
      if (!counter || counter.expiresAt <= now) {
        counter = {
          count: 0,
          expiresAt: now
        };
        fixedCounters.set(options.key, counter);
      }
      if (options.limit && counter.count + options.tokens > options.limit) {
        return [counter.count + options.tokens, counter.expiresAt - now];
      }
      counter.count += options.tokens;
      counter.expiresAt += refillRateMillis * options.tokens;
      return [counter.count, counter.expiresAt - now];
    })),
    tokenBucket: options => Effect.clockWith(clock => Effect.sync(() => {
      const refillRateMillis = Duration.toMillis(options.refillRate);
      const now = clock.unsafeCurrentTimeMillis();
      let bucket = tokenBuckets.get(options.key);
      if (!bucket) {
        bucket = {
          tokens: options.limit,
          lastRefill: now
        };
        tokenBuckets.set(options.key, bucket);
      } else {
        const elapsed = now - bucket.lastRefill;
        const tokensToAdd = Math.floor(elapsed / refillRateMillis);
        if (tokensToAdd > 0) {
          bucket.tokens = Math.min(options.limit, bucket.tokens + tokensToAdd);
          bucket.lastRefill += tokensToAdd * refillRateMillis;
        }
      }
      const newTokenCount = bucket.tokens - options.tokens;
      if (options.allowOverflow || newTokenCount >= 0) {
        bucket.tokens = newTokenCount;
      }
      return newTokenCount;
    }))
  });
});
//# sourceMappingURL=RateLimiter.js.map