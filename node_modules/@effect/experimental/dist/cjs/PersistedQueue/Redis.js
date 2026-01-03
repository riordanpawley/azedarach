"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.make = exports.layerStoreConfig = exports.layerStore = void 0;
var Cause = _interopRequireWildcard(require("effect/Cause"));
var Config = _interopRequireWildcard(require("effect/Config"));
var Duration = _interopRequireWildcard(require("effect/Duration"));
var Effect = _interopRequireWildcard(require("effect/Effect"));
var Exit = _interopRequireWildcard(require("effect/Exit"));
var Layer = _interopRequireWildcard(require("effect/Layer"));
var Mailbox = _interopRequireWildcard(require("effect/Mailbox"));
var MutableRef = _interopRequireWildcard(require("effect/MutableRef"));
var Option = _interopRequireWildcard(require("effect/Option"));
var RcMap = _interopRequireWildcard(require("effect/RcMap"));
var Schedule = _interopRequireWildcard(require("effect/Schedule"));
var _ioredis = require("ioredis");
var PersistedQueue = _interopRequireWildcard(require("../PersistedQueue.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 */

/**
 * @since 1.0.0
 * @category Constructors
 */
const make = exports.make = /*#__PURE__*/Effect.fnUntraced(function* (options) {
  const pollInterval = options.pollInterval ? Duration.decode(options.pollInterval) : Duration.seconds(1);
  const redis = yield* Effect.acquireRelease(Effect.sync(() => new _ioredis.Redis(options)), redis => Effect.promise(() => redis.quit()));
  redis.defineCommand("offer", {
    lua: `
local key_queue = KEYS[1]
local key_ids = KEYS[2]
local id = ARGV[1]
local payload = ARGV[2]

local result = redis.call("SADD", key_ids, id)
if result == 1 then
  redis.call("RPUSH", key_queue, payload)
end
`,
    numberOfKeys: 2,
    readOnly: false
  });
  redis.defineCommand("resetQueue", {
    lua: `
local key_queue = KEYS[1]
local key_pending = KEYS[2]
local prefix = ARGV[1]

local entries = redis.call("HGETALL", key_pending)
for id, payload in pairs(entries) do
  local lock_key = prefix .. id .. ":lock"
  local exists = redis.call("EXISTS", lock_key)
  if exists == 0 then
    redis.call("RPUSH", key_queue, payload)
    redis.call("HDEL", key_pending, id)
  end
end
`,
    numberOfKeys: 2,
    readOnly: false
  });
  redis.defineCommand("requeue", {
    lua: `
local key_queue = KEYS[1]
local key_pending = KEYS[2]
local key_lock = KEYS[3]
local id = ARGV[1]
local payload = ARGV[2]

redis.call("DEL", key_lock)
redis.call("HDEL", key_pending, id)
redis.call("RPUSH", key_queue, payload)
`,
    numberOfKeys: 3,
    readOnly: false
  });
  redis.defineCommand("complete", {
    lua: `
local key_pending = KEYS[1]
local key_lock = KEYS[2]
local id = ARGV[1]

redis.call("DEL", key_lock)
redis.call("HDEL", key_pending, id)
`,
    numberOfKeys: 2,
    readOnly: false
  });
  redis.defineCommand("failed", {
    lua: `
local key_pending = KEYS[1]
local key_lock = KEYS[2]
local key_failed = KEYS[3]
local id = ARGV[1]
local payload = ARGV[2]

redis.call("DEL", key_lock)
redis.call("HDEL", key_pending, id)
redis.call("RPUSH", key_failed, payload)
`,
    numberOfKeys: 2,
    readOnly: false
  });
  redis.defineCommand("take", {
    lua: `
local key_queue = KEYS[1]
local key_pending = KEYS[2]
local prefix = ARGV[1]
local worker_id = ARGV[2]
local batch_size = tonumber(ARGV[3])
local pttl = ARGV[4]

local payloads = redis.call("LPOP", key_queue, batch_size)
if not payloads then
  return nil
end

for i, payload in ipairs(payloads) do
  local id = cjson.decode(payload).id
  local key_lock = prefix .. id .. ":lock"
  redis.call("SET", key_lock, worker_id, "PX", pttl)
  redis.call("HSET", key_pending, id, payload)
end

return payloads
`,
    numberOfKeys: 2,
    readOnly: false
  });
  const lockRefreshMillis = options.lockRefreshInterval ? Duration.toMillis(options.lockRefreshInterval) : 30_000;
  const lockExpirationMillis = options.lockExpiration ? Duration.toMillis(options.lockExpiration) : 90_000;
  const prefix = options.prefix ?? "effectq:";
  const keyQueue = name => `${prefix}${name}`;
  const keyLock = id => `${prefix}${id}:lock`;
  const keyPending = name => `${prefix}${name}:pending`;
  const keyFailed = name => `${prefix}${name}:failed`;
  const workerId = crypto.randomUUID();
  const mailboxes = yield* RcMap.make({
    lookup: Effect.fnUntraced(function* (name) {
      const queueKey = keyQueue(name);
      const pendingKey = keyPending(name);
      const mailbox = yield* Mailbox.make();
      const takers = MutableRef.make(0);
      const pollLatch = Effect.unsafeMakeLatch();
      const takenLatch = Effect.unsafeMakeLatch();
      yield* Effect.addFinalizer(() => Effect.flatMap(mailbox.clear, elements => elements.length === 0 ? Effect.void : Effect.promise(() => Promise.all(Array.from(elements, element => redis.requeue(queueKey, pendingKey, keyLock(element.id), element.id, JSON.stringify(element)))))));
      yield* Effect.sync(() => {
        redis.resetQueue(queueKey, pendingKey, prefix);
      }).pipe(Effect.andThen(Effect.sleep(lockRefreshMillis)), Effect.forever, Effect.forkScoped, Effect.interruptible);
      const poll = size => Effect.promise(() => redis.take(queueKey, pendingKey, prefix, workerId, size, lockExpirationMillis));
      yield* Effect.gen(function* () {
        while (true) {
          yield* pollLatch.await;
          yield* Effect.yieldNow();
          const results = takers.current === 0 ? null : yield* poll(takers.current);
          if (results === null) {
            yield* Effect.sleep(pollInterval);
            continue;
          }
          takenLatch.unsafeClose();
          yield* mailbox.offerAll(results.map(json => JSON.parse(json)));
          yield* takenLatch.await;
          yield* Effect.yieldNow();
        }
      }).pipe(Effect.sandbox, Effect.retry(Schedule.spaced(500)), Effect.forkScoped, Effect.interruptible);
      return {
        mailbox,
        takers,
        pollLatch,
        takenLatch
      };
    }),
    idleTimeToLive: Duration.seconds(30)
  });
  const activeLockKeys = new Set();
  yield* Effect.gen(function* () {
    while (true) {
      yield* Effect.sleep(lockRefreshMillis);
      activeLockKeys.forEach(key => {
        redis.pexpire(key, lockExpirationMillis);
      });
    }
  }).pipe(Effect.forkScoped, Effect.interruptible, Effect.annotateLogs({
    package: "@effect/experimental",
    module: "PersistedQueue/Redis",
    fiber: "refreshLocks"
  }));
  return PersistedQueue.PersistedQueueStore.of({
    offer: ({
      element,
      id,
      isCustomId,
      name
    }) => Effect.tryPromise({
      try: () => isCustomId ? redis.offer(`${prefix}${name}`, `${prefix}${name}:ids`, id, JSON.stringify({
        id,
        element,
        attempts: 0
      })) : redis.lpush(`${prefix}${name}`, JSON.stringify({
        id,
        element,
        attempts: 0
      })),
      catch: cause => new PersistedQueue.PersistedQueueError({
        message: "Failed to offer element to persisted queue",
        cause
      })
    }),
    take: options => Effect.uninterruptibleMask(restore => RcMap.get(mailboxes, options.name).pipe(Effect.flatMap(({
      mailbox,
      pollLatch,
      takenLatch,
      takers
    }) => {
      takers.current++;
      if (takers.current === 1) {
        pollLatch.unsafeOpen();
      }
      return Effect.tap(restore(mailbox.take), () => {
        takers.current--;
        if (takers.current === 0) {
          pollLatch.unsafeClose();
          takenLatch.unsafeOpen();
        } else if (Option.getOrUndefined(mailbox.unsafeSize()) === 0) {
          takenLatch.unsafeOpen();
        }
      });
    }), Effect.scoped, Effect.tap(element => {
      const lock = keyLock(element.id);
      activeLockKeys.add(lock);
      return Effect.addFinalizer(Exit.match({
        onFailure: cause => {
          activeLockKeys.delete(lock);
          const nextAttempts = element.attempts + 1;
          if (nextAttempts >= options.maxAttempts) {
            return Effect.promise(() => redis.failed(keyPending(options.name), lock, keyFailed(options.name), element.id, JSON.stringify({
              ...element,
              lastFailure: Cause.pretty(cause, {
                renderErrorCause: true
              }),
              attempts: nextAttempts
            })));
          }
          return Effect.promise(() => redis.requeue(keyQueue(options.name), keyPending(options.name), lock, element.id, JSON.stringify(Cause.isInterruptedOnly(cause) ? element : {
            ...element,
            lastFailure: Cause.pretty(cause, {
              renderErrorCause: true
            }),
            attempts: nextAttempts
          })));
        },
        onSuccess: () => {
          activeLockKeys.delete(lock);
          return Effect.promise(() => redis.complete(keyPending(options.name), lock, element.id));
        }
      }));
    })))
  });
});
/**
 * @since 1.0.0
 * @category Layers
 */
const layerStore = options => Layer.scoped(PersistedQueue.PersistedQueueStore, make(options));
/**
 * @since 1.0.0
 * @category Layers
 */
exports.layerStore = layerStore;
const layerStoreConfig = options => Layer.scoped(PersistedQueue.PersistedQueueStore, Effect.flatMap(Config.unwrap(options), make));
exports.layerStoreConfig = layerStoreConfig;
//# sourceMappingURL=Redis.js.map