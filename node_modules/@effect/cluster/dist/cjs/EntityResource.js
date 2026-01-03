"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.makeK8sPod = exports.make = exports.TypeId = exports.CloseScope = void 0;
var Context = _interopRequireWildcard(require("effect/Context"));
var Duration = _interopRequireWildcard(require("effect/Duration"));
var Effect = _interopRequireWildcard(require("effect/Effect"));
var _Function = require("effect/Function");
var RcRef = _interopRequireWildcard(require("effect/RcRef"));
var Scope = _interopRequireWildcard(require("effect/Scope"));
var Entity = _interopRequireWildcard(require("./Entity.js"));
var K8sHttpClient = _interopRequireWildcard(require("./K8sHttpClient.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 */

/**
 * @since 1.0.0
 * @category Type ids
 */
const TypeId = exports.TypeId = "~@effect/cluster/EntityResource";
/**
 * A `Scope` that is only closed when the resource is explicitly closed.
 *
 * It is not closed during restarts, due to shard movement or node shutdowns.
 *
 * @since 1.0.0
 * @category Scope
 */
class CloseScope extends /*#__PURE__*/Context.Tag("@effect/cluster/EntityResource/CloseScope")() {}
/**
 * A `EntityResource` is a resource that can be acquired inside a cluster
 * entity, which will keep the entity alive even across restarts.
 *
 * The resource will only be fully released when the idle time to live is
 * reached, or when the `close` effect is called.
 *
 * By default, the `idleTimeToLive` is infinite, meaning the resource will only
 * be released when `close` is called.
 *
 * @since 1.0.0
 * @category Constructors
 */
exports.CloseScope = CloseScope;
const make = exports.make = /*#__PURE__*/Effect.fnUntraced(function* (options) {
  let shuttingDown = false;
  yield* Entity.keepAlive(true);
  const ref = yield* RcRef.make({
    acquire: Effect.gen(function* () {
      const closeable = yield* Scope.make();
      yield* Effect.addFinalizer(Effect.fnUntraced(function* (exit) {
        if (shuttingDown) return;
        yield* Scope.close(closeable, exit);
        yield* Entity.keepAlive(false);
      }));
      return yield* options.acquire.pipe(Effect.provideService(CloseScope, closeable));
    }),
    idleTimeToLive: options.idleTimeToLive ?? Duration.infinity
  });
  yield* Effect.addFinalizer(() => {
    shuttingDown = true;
    return Effect.void;
  });
  // Initialize the resource
  yield* Effect.scoped(RcRef.get(ref));
  return (0, _Function.identity)({
    [TypeId]: TypeId,
    get: RcRef.get(ref),
    close: RcRef.invalidate(ref)
  });
});
/**
 * @since 1.0.0
 * @category Kubernetes
 */
const makeK8sPod = exports.makeK8sPod = /*#__PURE__*/Effect.fnUntraced(function* (spec, options) {
  const createPod = yield* K8sHttpClient.makeCreatePod;
  return yield* make({
    ...options,
    acquire: Effect.gen(function* () {
      const scope = yield* CloseScope;
      return yield* createPod(spec).pipe(Scope.extend(scope));
    })
  });
});
//# sourceMappingURL=EntityResource.js.map