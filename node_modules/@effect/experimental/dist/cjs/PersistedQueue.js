"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.makeFactory = exports.make = exports.layerStoreMemory = exports.layer = exports.TypeId = exports.PersistedQueueStore = exports.PersistedQueueFactory = exports.PersistedQueueError = exports.ErrorTypeId = void 0;
var Context = _interopRequireWildcard(require("effect/Context"));
var Effect = _interopRequireWildcard(require("effect/Effect"));
var Exit = _interopRequireWildcard(require("effect/Exit"));
var Iterable = _interopRequireWildcard(require("effect/Iterable"));
var Layer = _interopRequireWildcard(require("effect/Layer"));
var Schema = _interopRequireWildcard(require("effect/Schema"));
var Scope = _interopRequireWildcard(require("effect/Scope"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 */

/**
 * @since 1.0.0
 * @category Type IDs
 */
const TypeId = exports.TypeId = "~@effect/experimental/PersistedQueue";
/**
 * @since 1.0.0
 * @category Factory
 */
class PersistedQueueFactory extends /*#__PURE__*/Context.Tag("@effect/experimental/PersistedQueue/PersistedQueueFactory")() {}
/**
 * @since 1.0.0
 * @category Accessors
 */
exports.PersistedQueueFactory = PersistedQueueFactory;
const make = options => Effect.flatMap(PersistedQueueFactory, factory => factory.make(options));
/**
 * @since 1.0.0
 * @category Factory
 */
exports.make = make;
const makeFactory = exports.makeFactory = /*#__PURE__*/Effect.gen(function* () {
  const store = yield* PersistedQueueStore;
  return PersistedQueueFactory.of({
    make(options) {
      const encodeUnknown = Schema.encodeUnknown(options.schema);
      const decodeUnknown = Schema.decodeUnknown(options.schema);
      return Effect.succeed({
        [TypeId]: TypeId,
        offer: (value, opts) => Effect.flatMap(encodeUnknown(value), element => {
          const id = opts?.id ?? crypto.randomUUID();
          return Effect.as(store.offer({
            name: options.name,
            id,
            element,
            isCustomId: opts?.id !== undefined
          }), id);
        }),
        take: (f, opts) => Effect.uninterruptibleMask(Effect.fnUntraced(function* (restore) {
          const scope = yield* Scope.make();
          const item = yield* store.take({
            name: options.name,
            maxAttempts: opts?.maxAttempts ?? 10
          }).pipe(Scope.extend(scope), restore);
          const exit = yield* decodeUnknown(item.element).pipe(Effect.flatMap(value => f(value, {
            id: item.id,
            attempts: item.attempts
          })), restore, Effect.exit);
          yield* Scope.close(scope, exit);
          return yield* exit;
        }))
      });
    }
  });
});
/**
 * @since 1.0.0
 * @category Factory
 */
const layer = exports.layer = /*#__PURE__*/Layer.effect(PersistedQueueFactory, makeFactory);
/**
 * @since 1.0.0
 * @category Errors
 */
const ErrorTypeId = exports.ErrorTypeId = "~@effect/experimental/PersistedQueue/PersistedQueueError";
/**
 * @since 1.0.0
 * @category Errors
 */
class PersistedQueueError extends /*#__PURE__*/Schema.TaggedError("@effect/experimental/PersistedQueue/PersistedQueueError")("PersistedQueueError", {
  message: Schema.String,
  cause: /*#__PURE__*/Schema.optional(Schema.Defect)
}) {
  /**
   * @since 1.0.0
   */
  [ErrorTypeId] = ErrorTypeId;
}
/**
 * @since 1.0.0
 * @category Store
 */
exports.PersistedQueueError = PersistedQueueError;
class PersistedQueueStore extends /*#__PURE__*/Context.Tag("@effect/experimental/PersistedQueue/PersistedQueueStore")() {}
/**
 * @since 1.0.0
 * @category Store
 */
exports.PersistedQueueStore = PersistedQueueStore;
const layerStoreMemory = exports.layerStoreMemory = /*#__PURE__*/Layer.sync(PersistedQueueStore, () => {
  const ids = new Set();
  const queues = new Map();
  const getOrCreateQueue = name => {
    let queue = queues.get(name);
    if (!queue) {
      queue = {
        latch: Effect.unsafeMakeLatch(false),
        items: new Set()
      };
      queues.set(name, queue);
    }
    return queue;
  };
  return PersistedQueueStore.of({
    offer: options => Effect.sync(() => {
      if (ids.has(options.id)) return;
      ids.add(options.id);
      const queue = getOrCreateQueue(options.name);
      queue.items.add({
        id: options.id,
        attempts: 0,
        element: options.element
      });
      queue.latch.unsafeOpen();
    }),
    take: Effect.fnUntraced(function* (options) {
      const queue = getOrCreateQueue(options.name);
      while (true) {
        yield* queue.latch.await;
        const item = Iterable.unsafeHead(queue.items);
        queue.items.delete(item);
        if (queue.items.size === 0) {
          queue.latch.unsafeClose();
        }
        yield* Effect.addFinalizer(exit => {
          if (exit._tag === "Success") {
            return Effect.void;
          } else if (!Exit.isInterrupted(exit)) {
            item.attempts += 1;
          }
          if (item.attempts >= options.maxAttempts) {
            return Effect.void;
          }
          queue.items.add(item);
          queue.latch.unsafeOpen();
          return Effect.void;
        });
        return item;
      }
    })
  });
});
//# sourceMappingURL=PersistedQueue.js.map