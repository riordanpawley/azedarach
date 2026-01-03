/**
 * @since 1.0.0
 */
import * as Context from "effect/Context";
import * as Duration from "effect/Duration";
import * as Effect from "effect/Effect";
import { identity } from "effect/Function";
import * as RcRef from "effect/RcRef";
import * as Scope from "effect/Scope";
import * as Entity from "./Entity.js";
import * as K8sHttpClient from "./K8sHttpClient.js";
/**
 * @since 1.0.0
 * @category Type ids
 */
export const TypeId = "~@effect/cluster/EntityResource";
/**
 * A `Scope` that is only closed when the resource is explicitly closed.
 *
 * It is not closed during restarts, due to shard movement or node shutdowns.
 *
 * @since 1.0.0
 * @category Scope
 */
export class CloseScope extends /*#__PURE__*/Context.Tag("@effect/cluster/EntityResource/CloseScope")() {}
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
export const make = /*#__PURE__*/Effect.fnUntraced(function* (options) {
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
  return identity({
    [TypeId]: TypeId,
    get: RcRef.get(ref),
    close: RcRef.invalidate(ref)
  });
});
/**
 * @since 1.0.0
 * @category Kubernetes
 */
export const makeK8sPod = /*#__PURE__*/Effect.fnUntraced(function* (spec, options) {
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