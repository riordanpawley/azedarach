"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.makePing = exports.makeK8s = exports.layerPing = exports.layerNoop = exports.layerK8s = exports.RunnerHealth = void 0;
var Context = _interopRequireWildcard(require("effect/Context"));
var Effect = _interopRequireWildcard(require("effect/Effect"));
var Layer = _interopRequireWildcard(require("effect/Layer"));
var Schedule = _interopRequireWildcard(require("effect/Schedule"));
var K8s = _interopRequireWildcard(require("./K8sHttpClient.js"));
var Runners = _interopRequireWildcard(require("./Runners.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 */

/**
 * Represents the service used to check if a Runner is healthy.
 *
 * If a Runner is responsive, shards will not be re-assigned because the Runner may
 * still be processing messages. If a Runner is not responsive, then its
 * associated shards can and will be re-assigned to a different Runner.
 *
 * @since 1.0.0
 * @category models
 */
class RunnerHealth extends /*#__PURE__*/Context.Tag("@effect/cluster/RunnerHealth")() {}
/**
 * A layer which will **always** consider a Runner healthy.
 *
 * This is useful for testing.
 *
 * @since 1.0.0
 * @category layers
 */
exports.RunnerHealth = RunnerHealth;
const layerNoop = exports.layerNoop = /*#__PURE__*/Layer.succeed(RunnerHealth, {
  isAlive: () => Effect.succeed(true)
});
/**
 * @since 1.0.0
 * @category Constructors
 */
const makePing = exports.makePing = /*#__PURE__*/Effect.gen(function* () {
  const runners = yield* Runners.Runners;
  const schedule = Schedule.spaced(500);
  function isAlive(address) {
    return runners.ping(address).pipe(Effect.timeout(10_000), Effect.retry({
      times: 5,
      schedule
    }), Effect.isSuccess);
  }
  return RunnerHealth.of({
    isAlive
  });
});
/**
 * A layer which will ping a Runner directly to check if it is healthy.
 *
 * @since 1.0.0
 * @category layers
 */
const layerPing = exports.layerPing = /*#__PURE__*/Layer.scoped(RunnerHealth, makePing);
/**
 * @since 1.0.0
 * @category Constructors
 */
const makeK8s = exports.makeK8s = /*#__PURE__*/Effect.fnUntraced(function* (options) {
  const allPods = yield* K8s.makeGetPods(options);
  return RunnerHealth.of({
    isAlive: address => allPods.pipe(Effect.map(pods => pods.get(address.host)?.isReadyOrInitializing ?? false), Effect.catchAllCause(() => Effect.succeed(true)))
  });
});
/**
 * A layer which will check the Kubernetes API to see if a Runner is healthy.
 *
 * The provided HttpClient will need to add the pod's CA certificate to its
 * trusted root certificates in order to communicate with the Kubernetes API.
 *
 * The pod service account will also need to have permissions to list pods in
 * order to use this layer.
 *
 * @since 1.0.0
 * @category layers
 */
const layerK8s = options => Layer.effect(RunnerHealth, makeK8s(options));
exports.layerK8s = layerK8s;
//# sourceMappingURL=RunnerHealth.js.map