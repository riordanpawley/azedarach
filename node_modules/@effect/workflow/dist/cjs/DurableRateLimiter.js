"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.rateLimit = void 0;
var RateLimiter = _interopRequireWildcard(require("@effect/experimental/RateLimiter"));
var Effect = _interopRequireWildcard(require("effect/Effect"));
var Activity = _interopRequireWildcard(require("./Activity.js"));
var DurableClock = _interopRequireWildcard(require("./DurableClock.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 */

/**
 * @since 1.0.0
 * @category Accessors
 */
const rateLimit = options => Activity.make({
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
exports.rateLimit = rateLimit;
//# sourceMappingURL=DurableRateLimiter.js.map