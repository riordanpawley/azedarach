"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.sleep = exports.make = exports.TypeId = void 0;
var Context = _interopRequireWildcard(require("effect/Context"));
var Duration = _interopRequireWildcard(require("effect/Duration"));
var Effect = _interopRequireWildcard(require("effect/Effect"));
var Activity = _interopRequireWildcard(require("./Activity.js"));
var DurableDeferred = _interopRequireWildcard(require("./DurableDeferred.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 */

/**
 * @since 1.0.0
 * @category Symbols
 */
const TypeId = exports.TypeId = /*#__PURE__*/Symbol.for("@effect/workflow/DurableClock");
/**
 * @since 1.0.0
 * @category Constructors
 */
const make = options => ({
  [TypeId]: TypeId,
  name: options.name,
  duration: Duration.decode(options.duration),
  deferred: DurableDeferred.make(`DurableClock/${options.name}`)
});
exports.make = make;
const EngineTag = /*#__PURE__*/Context.GenericTag("@effect/workflow/WorkflowEngine");
const InstanceTag = /*#__PURE__*/Context.GenericTag("@effect/workflow/WorkflowEngine/WorkflowInstance");
/**
 * @since 1.0.0
 * @category Sleeping
 */
const sleep = exports.sleep = /*#__PURE__*/Effect.fnUntraced(function* (options) {
  const duration = Duration.decode(options.duration);
  if (Duration.isZero(duration)) {
    return;
  }
  const inMemoryThreshold = options.inMemoryThreshold ? Duration.decode(options.inMemoryThreshold) : defaultInMemoryThreshold;
  if (Duration.lessThanOrEqualTo(duration, inMemoryThreshold)) {
    return yield* Activity.make({
      name: `DurableClock/${options.name}`,
      execute: Effect.sleep(duration)
    });
  }
  const engine = yield* EngineTag;
  const instance = yield* InstanceTag;
  const clock = make(options);
  yield* engine.scheduleClock(instance.workflow, {
    executionId: instance.executionId,
    clock
  });
  return yield* DurableDeferred.await(clock.deferred);
});
const defaultInMemoryThreshold = /*#__PURE__*/Duration.seconds(60);
//# sourceMappingURL=DurableClock.js.map