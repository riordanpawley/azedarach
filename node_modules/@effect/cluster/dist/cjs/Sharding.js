"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.layer = exports.Sharding = void 0;
var RpcClient = _interopRequireWildcard(require("@effect/rpc/RpcClient"));
var _RpcMessage = require("@effect/rpc/RpcMessage");
var Arr = _interopRequireWildcard(require("effect/Array"));
var Cause = _interopRequireWildcard(require("effect/Cause"));
var Context = _interopRequireWildcard(require("effect/Context"));
var Duration = _interopRequireWildcard(require("effect/Duration"));
var Effect = _interopRequireWildcard(require("effect/Effect"));
var Either = _interopRequireWildcard(require("effect/Either"));
var Equal = _interopRequireWildcard(require("effect/Equal"));
var Fiber = _interopRequireWildcard(require("effect/Fiber"));
var FiberMap = _interopRequireWildcard(require("effect/FiberMap"));
var FiberRef = _interopRequireWildcard(require("effect/FiberRef"));
var FiberSet = _interopRequireWildcard(require("effect/FiberSet"));
var _Function = require("effect/Function");
var HashMap = _interopRequireWildcard(require("effect/HashMap"));
var HashRing = _interopRequireWildcard(require("effect/HashRing"));
var Layer = _interopRequireWildcard(require("effect/Layer"));
var MutableHashMap = _interopRequireWildcard(require("effect/MutableHashMap"));
var MutableHashSet = _interopRequireWildcard(require("effect/MutableHashSet"));
var MutableRef = _interopRequireWildcard(require("effect/MutableRef"));
var Option = _interopRequireWildcard(require("effect/Option"));
var PubSub = _interopRequireWildcard(require("effect/PubSub"));
var Schedule = _interopRequireWildcard(require("effect/Schedule"));
var Scope = _interopRequireWildcard(require("effect/Scope"));
var Stream = _interopRequireWildcard(require("effect/Stream"));
var _ClusterError = require("./ClusterError.js");
var ClusterMetrics = _interopRequireWildcard(require("./ClusterMetrics.js"));
var _ClusterSchema = _interopRequireWildcard(require("./ClusterSchema.js"));
var ClusterSchema = _ClusterSchema;
var _EntityAddress = require("./EntityAddress.js");
var _EntityId = require("./EntityId.js");
var Envelope = _interopRequireWildcard(require("./Envelope.js"));
var EntityManager = _interopRequireWildcard(require("./internal/entityManager.js"));
var _entityReaper = require("./internal/entityReaper.js");
var _fiber = require("./internal/fiber.js");
var _hash = require("./internal/hash.js");
var _interruptors = require("./internal/interruptors.js");
var _resourceMap = require("./internal/resourceMap.js");
var Message = _interopRequireWildcard(require("./Message.js"));
var MessageStorage = _interopRequireWildcard(require("./MessageStorage.js"));
var Reply = _interopRequireWildcard(require("./Reply.js"));
var _Runner = require("./Runner.js");
var RunnerHealth = _interopRequireWildcard(require("./RunnerHealth.js"));
var _Runners = require("./Runners.js");
var _RunnerStorage = require("./RunnerStorage.js");
var _ShardId = require("./ShardId.js");
var _ShardingConfig = require("./ShardingConfig.js");
var _ShardingRegistrationEvent = require("./ShardingRegistrationEvent.js");
var _SingletonAddress = require("./SingletonAddress.js");
var Snowflake = _interopRequireWildcard(require("./Snowflake.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 * @category models
 */
class Sharding extends /*#__PURE__*/Context.Tag("@effect/cluster/Sharding")() {}
exports.Sharding = Sharding;
const make = /*#__PURE__*/Effect.gen(function* () {
  const config = yield* _ShardingConfig.ShardingConfig;
  const clock = yield* Effect.clock;
  const runnersService = yield* _Runners.Runners;
  const runnerHealth = yield* RunnerHealth.RunnerHealth;
  const snowflakeGen = yield* Snowflake.Generator;
  const shardingScope = yield* Effect.scope;
  const isShutdown = MutableRef.make(false);
  const fiberSet = yield* FiberSet.make();
  const runFork = yield* FiberSet.runtime(fiberSet)().pipe(Effect.mapInputContext(context => Context.omit(Scope.Scope)(context)));
  const storage = yield* MessageStorage.MessageStorage;
  const storageEnabled = storage !== MessageStorage.noop;
  const runnerStorage = yield* _RunnerStorage.RunnerStorage;
  const entityManagers = new Map();
  const shardAssignments = MutableHashMap.empty();
  const selfShards = MutableHashSet.empty();
  // the active shards are the ones that we have acquired the lock for
  const acquiredShards = MutableHashSet.empty();
  const activeShardsLatch = yield* Effect.makeLatch(false);
  const events = yield* PubSub.unbounded();
  const getRegistrationEvents = Stream.fromPubSub(events);
  const isLocalRunner = address => Option.isSome(config.runnerAddress) && Equal.equals(address, config.runnerAddress.value);
  function getShardId(entityId, group) {
    const id = Math.abs((0, _hash.hashString)(entityId) % config.shardsPerGroup) + 1;
    return (0, _ShardId.make)(group, id);
  }
  function isEntityOnLocalShards(address) {
    return MutableHashSet.has(acquiredShards, address.shardId);
  }
  yield* Scope.addFinalizer(shardingScope, Effect.logDebug("Shutdown complete").pipe(Effect.annotateLogs({
    package: "@effect/cluster",
    module: "Sharding"
  })));
  // --- Shard acquisition ---
  //
  // Responsible for acquiring and releasing shards from RunnerStorage.
  //
  // This should be shutdown last, when all entities have been shutdown, to
  // allow them to move to another runner.
  const releasingShards = MutableHashSet.empty();
  if (Option.isSome(config.runnerAddress)) {
    const selfAddress = config.runnerAddress.value;
    yield* Scope.addFinalizerExit(shardingScope, () => {
      // the locks expire over time, so if this fails we ignore it
      return Effect.ignore(runnerStorage.releaseAll(selfAddress));
    });
    const releaseShardsMap = yield* FiberMap.make();
    const releaseShard = Effect.fnUntraced(function* (shardId) {
      const fibers = Arr.empty();
      for (const state of entityManagers.values()) {
        if (state.status === "closed") continue;
        fibers.push(yield* Effect.fork(state.manager.interruptShard(shardId)));
      }
      yield* (0, _fiber.joinAllDiscard)(fibers);
      yield* runnerStorage.release(selfAddress, shardId);
      MutableHashSet.remove(releasingShards, shardId);
      yield* storage.unregisterShardReplyHandlers(shardId);
    }, Effect.sandbox, (effect, shardId) => effect.pipe(Effect.tapError(cause => Effect.logDebug(`Could not release shard, retrying`, cause).pipe(Effect.annotateLogs({
      package: "@effect/cluster",
      module: "Sharding",
      fiber: "releaseShard",
      runner: selfAddress,
      shardId
    }))), Effect.eventually, FiberMap.run(releaseShardsMap, shardId, {
      onlyIfMissing: true
    })));
    const releaseShards = Effect.gen(function* () {
      for (const shardId of releasingShards) {
        if (FiberMap.unsafeHas(releaseShardsMap, shardId)) continue;
        yield* releaseShard(shardId);
      }
    });
    yield* Effect.gen(function* () {
      activeShardsLatch.unsafeOpen();
      while (true) {
        yield* activeShardsLatch.await;
        activeShardsLatch.unsafeClose();
        // if a shard is no longer assigned to this runner, we release it
        for (const shardId of acquiredShards) {
          if (MutableHashSet.has(selfShards, shardId)) continue;
          MutableHashSet.remove(acquiredShards, shardId);
          MutableHashSet.add(releasingShards, shardId);
        }
        if (MutableHashSet.size(releasingShards) > 0) {
          yield* Effect.forkIn(syncSingletons, shardingScope);
          yield* releaseShards;
        }
        // if a shard has been assigned to this runner, we acquire it
        const unacquiredShards = MutableHashSet.empty();
        for (const shardId of selfShards) {
          if (MutableHashSet.has(acquiredShards, shardId) || MutableHashSet.has(releasingShards, shardId)) continue;
          MutableHashSet.add(unacquiredShards, shardId);
        }
        if (MutableHashSet.size(unacquiredShards) === 0) {
          continue;
        }
        const oacquired = yield* runnerStorage.acquire(selfAddress, unacquiredShards).pipe(Effect.timeoutOption(config.shardLockRefreshInterval));
        if (Option.isNone(oacquired)) {
          activeShardsLatch.unsafeOpen();
          continue;
        }
        const acquired = oacquired.value;
        yield* storage.resetShards(acquired).pipe(Effect.ignore, Effect.timeoutOption(config.shardLockRefreshInterval));
        for (const shardId of acquired) {
          if (MutableHashSet.has(releasingShards, shardId) || !MutableHashSet.has(selfShards, shardId)) {
            continue;
          }
          MutableHashSet.add(acquiredShards, shardId);
        }
        if (acquired.length > 0) {
          yield* storageReadLatch.open;
          yield* Effect.forkIn(syncSingletons, shardingScope);
          // update metrics
          ClusterMetrics.shards.unsafeUpdate(BigInt(MutableHashSet.size(acquiredShards)), []);
        }
        yield* Effect.sleep(1000);
        activeShardsLatch.unsafeOpen();
      }
    }).pipe(Effect.catchAllCause(cause => Effect.logWarning("Could not acquire/release shards", cause)), Effect.repeat(Schedule.spaced(config.entityMessagePollInterval)), Effect.annotateLogs({
      package: "@effect/cluster",
      module: "Sharding",
      fiber: "Shard acquisition loop",
      runner: selfAddress
    }), Effect.forkIn(shardingScope), Effect.interruptible);
    // refresh the shard locks every `shardLockRefreshInterval`
    yield* Effect.suspend(() => runnerStorage.refresh(selfAddress, [...acquiredShards, ...releasingShards])).pipe(Effect.flatMap(acquired => {
      for (const shardId of acquiredShards) {
        if (!acquired.includes(shardId)) {
          MutableHashSet.remove(acquiredShards, shardId);
          MutableHashSet.add(releasingShards, shardId);
        }
      }
      for (let i = 0; i < acquired.length; i++) {
        const shardId = acquired[i];
        if (!MutableHashSet.has(selfShards, shardId)) {
          MutableHashSet.remove(acquiredShards, shardId);
          MutableHashSet.add(releasingShards, shardId);
        }
      }
      return MutableHashSet.size(releasingShards) > 0 ? activeShardsLatch.open : Effect.void;
    }), Effect.retry({
      times: 5,
      schedule: Schedule.spaced(50)
    }), Effect.catchAllCause(cause => Effect.logError("Could not refresh shard locks", cause).pipe(Effect.andThen(clearSelfShards))), Effect.repeat(Schedule.fixed(config.shardLockRefreshInterval)), Effect.forever, Effect.forkIn(shardingScope), Effect.interruptible);
    // open the shard latch every poll interval
    yield* activeShardsLatch.open.pipe(Effect.delay(config.entityMessagePollInterval), Effect.forever, Effect.forkIn(shardingScope), Effect.interruptible);
  }
  const clearSelfShards = Effect.sync(() => {
    MutableHashSet.clear(selfShards);
    activeShardsLatch.unsafeOpen();
  });
  // --- Storage inbox ---
  //
  // Responsible for reading unprocessed messages from storage and sending them
  // to the appropriate entity manager.
  //
  // This should be shutdown before shard acquisition, to ensure no messages are
  // being processed before the shards are released.
  //
  // It should also be shutdown after the entity managers, to ensure interrupt
  // & ack envelopes can still be processed.
  const storageReadLatch = yield* Effect.makeLatch(true);
  const openStorageReadLatch = (0, _Function.constant)(storageReadLatch.open);
  const storageReadLock = Effect.unsafeMakeSemaphore(1);
  const withStorageReadLock = storageReadLock.withPermits(1);
  if (storageEnabled && Option.isSome(config.runnerAddress)) {
    const selfAddress = config.runnerAddress.value;
    const entityRegistrationTimeoutMillis = Duration.toMillis(config.entityRegistrationTimeout);
    const storageStartMillis = clock.unsafeCurrentTimeMillis();
    yield* Effect.gen(function* () {
      yield* Effect.logDebug("Starting");
      yield* Effect.addFinalizer(() => Effect.logDebug("Shutting down"));
      let index = 0;
      let messages = [];
      const removableNotifications = new Set();
      const resetAddresses = MutableHashSet.empty();
      const processMessages = Effect.whileLoop({
        while: () => index < messages.length,
        step: () => index++,
        body: () => send
      });
      const send = Effect.catchAllCause(Effect.suspend(() => {
        const message = messages[index];
        const address = message.envelope.address;
        if (!MutableHashSet.has(acquiredShards, address.shardId)) {
          return Effect.void;
        }
        const state = entityManagers.get(address.entityType);
        if (!state) {
          const sinceStart = clock.unsafeCurrentTimeMillis() - storageStartMillis;
          if (sinceStart < entityRegistrationTimeoutMillis) {
            // reset address in the case that the entity is slow to register
            MutableHashSet.add(resetAddresses, address);
            return Effect.void;
          }
          // if the entity did not register in time, we save a defect reply
          return Effect.die(new Error(`Entity type '${address.entityType}' not registered`));
        } else if (state.status === "closed") {
          return Effect.void;
        }
        const isProcessing = state.manager.isProcessingFor(message);
        if (message._tag === "IncomingEnvelope" && isProcessing) {
          // If the message might affect a currently processing request, we
          // send it to the entity manager to be processed.
          return state.manager.send(message);
        } else if (isProcessing || state.status === "closing") {
          // If the request is already processing, we skip it.
          // Or if the entity is closing, we skip all incoming messages.
          return Effect.void;
        } else if (message._tag === "IncomingRequest" && pendingNotifications.has(message.envelope.requestId)) {
          const entry = pendingNotifications.get(message.envelope.requestId);
          pendingNotifications.delete(message.envelope.requestId);
          removableNotifications.delete(entry);
          entry.resume(Effect.void);
        }
        // If the entity was resuming in another fiber, we add the message
        // id to the unprocessed set.
        const resumptionState = MutableHashMap.get(entityResumptionState, address);
        if (Option.isSome(resumptionState)) {
          resumptionState.value.unprocessed.add(message.envelope.requestId);
          if (message.envelope._tag === "Interrupt") {
            resumptionState.value.interrupts.set(message.envelope.requestId, message);
          }
          return Effect.void;
        }
        return state.manager.send(message);
      }), cause => {
        const message = messages[index];
        const error = Cause.failureOrCause(cause);
        // if we get a defect, then update storage
        if (Either.isRight(error)) {
          if (Cause.isInterrupted(cause)) {
            return Effect.void;
          }
          return Effect.ignore(storage.saveReply(Reply.ReplyWithContext.fromDefect({
            id: snowflakeGen.unsafeNext(),
            requestId: message.envelope.requestId,
            defect: Cause.squash(cause)
          })));
        }
        if (error.left._tag === "MailboxFull") {
          // MailboxFull can only happen for requests, so this cast is safe
          return resumeEntityFromStorage(message);
        }
        return Effect.void;
      });
      while (true) {
        // wait for the next poll interval, or if we get notified of a change
        yield* storageReadLatch.await;
        // if we get notified of a change, ensure we start a read immediately
        // next iteration
        storageReadLatch.unsafeClose();
        // the lock is used to ensure resuming entities have a garantee that no
        // more items are added to the unprocessed set while the semaphore is
        // acquired.
        yield* storageReadLock.take(1);
        entityManagers.forEach(state => state.manager.clearProcessed());
        if (pendingNotifications.size > 0) {
          pendingNotifications.forEach(entry => removableNotifications.add(entry));
        }
        messages = yield* storage.unprocessedMessages(acquiredShards);
        index = 0;
        yield* processMessages;
        if (removableNotifications.size > 0) {
          removableNotifications.forEach(({
            message,
            resume
          }) => {
            pendingNotifications.delete(message.envelope.requestId);
            resume(Effect.fail(new _ClusterError.EntityNotAssignedToRunner({
              address: message.envelope.address
            })));
          });
          removableNotifications.clear();
        }
        if (MutableHashSet.size(resetAddresses) > 0) {
          for (const address of resetAddresses) {
            yield* Effect.logWarning("Could not find entity manager for address, retrying").pipe(Effect.annotateLogs({
              address
            }));
            yield* Effect.forkIn(storage.resetAddress(address), shardingScope);
          }
          MutableHashSet.clear(resetAddresses);
        }
        // let the resuming entities check if they are done
        yield* storageReadLock.release(1);
      }
    }).pipe(Effect.scoped, Effect.ensuring(storageReadLock.releaseAll), Effect.catchAllCause(cause => Effect.logWarning("Could not read messages from storage", cause)), Effect.forever, Effect.annotateLogs({
      package: "@effect/cluster",
      module: "Sharding",
      fiber: "Storage read loop",
      runner: selfAddress
    }), Effect.withUnhandledErrorLogLevel(Option.none()), Effect.forkIn(shardingScope), Effect.interruptible);
    // open the storage latch every poll interval
    yield* storageReadLatch.open.pipe(Effect.delay(config.entityMessagePollInterval), Effect.forever, Effect.forkIn(shardingScope), Effect.interruptible);
    // Resume unprocessed messages for entities that reached a full mailbox.
    const entityResumptionState = MutableHashMap.empty();
    const resumeEntityFromStorage = lastReceivedMessage => {
      const address = lastReceivedMessage.envelope.address;
      const resumptionState = MutableHashMap.get(entityResumptionState, address);
      if (Option.isSome(resumptionState)) {
        resumptionState.value.unprocessed.add(lastReceivedMessage.envelope.requestId);
        return Effect.void;
      }
      MutableHashMap.set(entityResumptionState, address, {
        unprocessed: new Set([lastReceivedMessage.envelope.requestId]),
        interrupts: new Map()
      });
      return resumeEntityFromStorageImpl(address);
    };
    const resumeEntityFromStorageImpl = Effect.fnUntraced(function* (address) {
      const state = entityManagers.get(address.entityType);
      if (!state) {
        MutableHashMap.remove(entityResumptionState, address);
        return;
      }
      const resumptionState = Option.getOrThrow(MutableHashMap.get(entityResumptionState, address));
      let done = false;
      while (!done) {
        // if the shard is no longer assigned to this runner, we stop
        if (!MutableHashSet.has(acquiredShards, address.shardId)) {
          return;
        }
        // take a batch of unprocessed messages ids
        const messageIds = Arr.empty();
        for (const id of resumptionState.unprocessed) {
          if (messageIds.length === 1024) break;
          messageIds.push(id);
        }
        const messages = yield* storage.unprocessedMessagesById(messageIds);
        // this should not happen, but we handle it just in case
        if (messages.length === 0) {
          yield* Effect.sleep(config.entityMessagePollInterval);
          continue;
        }
        let index = 0;
        const sendWithRetry = Effect.catchTags(Effect.suspend(() => {
          if (!MutableHashSet.has(acquiredShards, address.shardId)) {
            return Effect.fail(new _ClusterError.EntityNotAssignedToRunner({
              address
            }));
          }
          const message = messages[index];
          // check if this is a request that was interrupted
          const interrupt = message._tag === "IncomingRequest" && resumptionState.interrupts.get(message.envelope.requestId);
          return interrupt ? Effect.flatMap(state.manager.send(message), () => {
            resumptionState.interrupts.delete(message.envelope.requestId);
            return state.manager.send(interrupt);
          }) : state.manager.send(message);
        }), {
          MailboxFull: () => Effect.delay(sendWithRetry, config.sendRetryInterval),
          AlreadyProcessingMessage: () => Effect.void
        });
        yield* Effect.whileLoop({
          while: () => index < messages.length,
          body: (0, _Function.constant)(sendWithRetry),
          step: () => index++
        });
        for (const id of messageIds) {
          resumptionState.unprocessed.delete(id);
        }
        if (resumptionState.unprocessed.size > 0) continue;
        // if we have caught up to the main storage loop, we let it take over
        yield* withStorageReadLock(Effect.sync(() => {
          if (resumptionState.unprocessed.size === 0) {
            MutableHashMap.remove(entityResumptionState, address);
            done = true;
          }
        }));
      }
    }, Effect.retry({
      while: e => e._tag === "PersistenceError",
      schedule: Schedule.spaced(config.entityMessagePollInterval)
    }), Effect.catchAllCause(cause => Effect.logDebug("Could not resume unprocessed messages", cause)), (effect, address) => Effect.annotateLogs(effect, {
      package: "@effect/cluster",
      module: "Sharding",
      fiber: "Resuming unprocessed messages",
      runner: selfAddress,
      entity: address
    }), (effect, address) => Effect.ensuring(effect, Effect.sync(() => MutableHashMap.remove(entityResumptionState, address))), Effect.withUnhandledErrorLogLevel(Option.none()), Effect.forkIn(shardingScope), Effect.interruptible);
  }
  // --- Sending messages ---
  const sendLocal = message => Effect.suspend(function loop() {
    const address = message.envelope.address;
    if (!isEntityOnLocalShards(address)) {
      return Effect.fail(new _ClusterError.EntityNotAssignedToRunner({
        address
      }));
    }
    const state = entityManagers.get(address.entityType);
    if (!state) {
      return Effect.flatMap(waitForEntityManager(address.entityType), loop);
    } else if (state.status === "closed" || state.status === "closing" && message._tag === "IncomingRequest") {
      // if we are shutting down, we don't accept new requests
      return Effect.fail(new _ClusterError.EntityNotAssignedToRunner({
        address
      }));
    }
    return message._tag === "IncomingRequest" || message._tag === "IncomingEnvelope" ? state.manager.send(message) : runnersService.sendLocal({
      message,
      send: state.manager.sendLocal,
      simulateRemoteSerialization: config.simulateRemoteSerialization
    });
  });
  const pendingNotifications = new Map();
  const notifyLocal = (message, discard, options) => Effect.suspend(function loop() {
    const address = message.envelope.address;
    const state = entityManagers.get(address.entityType);
    if (!state) {
      return Effect.flatMap(waitForEntityManager(address.entityType), loop);
    } else if (state.status === "closed" || !isEntityOnLocalShards(address)) {
      return Effect.fail(new _ClusterError.EntityNotAssignedToRunner({
        address
      }));
    }
    const isLocal = isEntityOnLocalShards(address);
    const notify = storageEnabled ? openStorageReadLatch : () => Effect.die("Sharding.notifyLocal: storage is disabled");
    if (message._tag === "IncomingRequest" || message._tag === "IncomingEnvelope") {
      if (!isLocal) {
        return Effect.fail(new _ClusterError.EntityNotAssignedToRunner({
          address
        }));
      } else if (message._tag === "IncomingRequest" && state.manager.isProcessingFor(message, {
        excludeReplies: true
      })) {
        return Effect.fail(new _ClusterError.AlreadyProcessingMessage({
          address,
          envelopeId: message.envelope.requestId
        }));
      } else if (message._tag === "IncomingRequest" && options?.waitUntilRead) {
        if (!storageEnabled) return notify();
        return Effect.async(resume => {
          let entry = pendingNotifications.get(message.envelope.requestId);
          if (entry) {
            const prevResume = entry.resume;
            entry.resume = effect => {
              prevResume(effect);
              resume(effect);
            };
            return;
          }
          entry = {
            resume,
            message
          };
          pendingNotifications.set(message.envelope.requestId, entry);
          storageReadLatch.unsafeOpen();
        });
      }
      return notify();
    }
    return runnersService.notifyLocal({
      message,
      notify,
      discard,
      storageOnly: !isLocal
    });
  });
  function sendOutgoing(message, discard, retries) {
    return Effect.catchIf(Effect.suspend(() => {
      const address = message.envelope.address;
      const isPersisted = Context.get(message.rpc.annotations, _ClusterSchema.Persisted);
      if (isPersisted && !storageEnabled) {
        return Effect.die("Sharding.sendOutgoing: Persisted messages require MessageStorage");
      }
      const maybeRunner = MutableHashMap.get(shardAssignments, address.shardId);
      const runnerIsLocal = Option.isSome(maybeRunner) && isLocalRunner(maybeRunner.value);
      if (isPersisted) {
        return runnerIsLocal ? notifyLocal(message, discard) : runnersService.notify({
          address: maybeRunner,
          message,
          discard
        });
      } else if (Option.isNone(maybeRunner)) {
        return Effect.fail(new _ClusterError.EntityNotAssignedToRunner({
          address
        }));
      }
      return runnerIsLocal ? sendLocal(message) : runnersService.send({
        address: maybeRunner.value,
        message
      });
    }), error => error._tag === "EntityNotAssignedToRunner" || error._tag === "RunnerUnavailable", error => {
      if (retries === 0) {
        return Effect.die(error);
      }
      return Effect.delay(sendOutgoing(message, discard, retries && retries - 1), config.sendRetryInterval);
    });
  }
  const reset = requestId => Effect.matchCause(storage.clearReplies(requestId), {
    onSuccess: () => true,
    onFailure: () => false
  });
  // --- RunnerStorage sync ---
  //
  // This is responsible for syncing the local view of runners and shard
  // assignments with RunnerStorage.
  //
  // It should be shutdown after the clients, so that they can still get correct
  // shard assignments for outgoing messages (they could still be in use by
  // entities that are shutting down).
  const selfRunner = Option.isSome(config.runnerAddress) ? new _Runner.Runner({
    address: config.runnerAddress.value,
    groups: config.shardGroups,
    weight: config.runnerShardWeight
  }) : undefined;
  let allRunners = MutableHashMap.empty();
  let healthyRunnerCount = 0;
  // update metrics
  if (selfRunner) {
    ClusterMetrics.runners.unsafeUpdate(BigInt(1), []);
    ClusterMetrics.runnersHealthy.unsafeUpdate(BigInt(1), []);
  }
  yield* Effect.gen(function* () {
    const hashRings = new Map();
    let nextRunners = MutableHashMap.empty();
    const healthyRunners = MutableHashSet.empty();
    const withTimeout = Effect.timeout(Duration.seconds(5));
    while (true) {
      // Ensure the current runner is registered
      if (selfRunner && !isShutdown.current && !MutableHashMap.has(allRunners, selfRunner)) {
        yield* Effect.logDebug("Registering runner", selfRunner);
        const machineId = yield* withTimeout(runnerStorage.register(selfRunner, true));
        yield* snowflakeGen.setMachineId(machineId);
      }
      const runners = yield* withTimeout(runnerStorage.getRunners);
      let changed = false;
      for (let i = 0; i < runners.length; i++) {
        const [runner, healthy] = runners[i];
        MutableHashMap.set(nextRunners, runner, healthy);
        const wasHealthy = MutableHashSet.has(healthyRunners, runner);
        if (!healthy || wasHealthy) {
          if (healthy === wasHealthy || !wasHealthy) {
            // no change
            MutableHashMap.remove(allRunners, runner);
          }
          continue;
        }
        changed = true;
        MutableHashSet.add(healthyRunners, runner);
        MutableHashMap.remove(allRunners, runner);
        for (let j = 0; j < runner.groups.length; j++) {
          const group = runner.groups[j];
          let ring = hashRings.get(group);
          if (!ring) {
            ring = HashRing.make();
            hashRings.set(group, ring);
          }
          HashRing.add(ring, runner.address, {
            weight: runner.weight
          });
        }
      }
      // Remove runners that are no longer present or healthy
      MutableHashMap.forEach(allRunners, (_, runner) => {
        changed = true;
        MutableHashMap.remove(allRunners, runner);
        MutableHashSet.remove(healthyRunners, runner);
        runFork(runnersService.onRunnerUnavailable(runner.address));
        for (let i = 0; i < runner.groups.length; i++) {
          HashRing.remove(hashRings.get(runner.groups[i]), runner.address);
        }
      });
      // swap allRunners and nextRunners
      const prevRunners = allRunners;
      allRunners = nextRunners;
      nextRunners = prevRunners;
      healthyRunnerCount = MutableHashSet.size(healthyRunners);
      // Ensure the current runner is registered
      if (selfRunner && !isShutdown.current && !MutableHashMap.has(allRunners, selfRunner)) {
        continue;
      }
      // Recompute shard assignments if the set of healthy runners has changed.
      if (changed) {
        MutableHashSet.clear(selfShards);
        hashRings.forEach((ring, group) => {
          const newAssignments = HashRing.getShards(ring, config.shardsPerGroup);
          for (let i = 0; i < config.shardsPerGroup; i++) {
            const shard = (0, _ShardId.make)(group, i + 1);
            if (newAssignments) {
              const runner = newAssignments[i];
              MutableHashMap.set(shardAssignments, shard, runner);
              if (isLocalRunner(runner)) {
                MutableHashSet.add(selfShards, shard);
              }
            } else {
              MutableHashMap.remove(shardAssignments, shard);
            }
          }
        });
        yield* Effect.logDebug("New shard assignments", selfShards);
        activeShardsLatch.unsafeOpen();
        // update metrics
        if (selfRunner) {
          ClusterMetrics.runnersHealthy.unsafeUpdate(BigInt(MutableHashSet.has(healthyRunners, selfRunner) ? 1 : 0), []);
        }
      }
      if (selfRunner && MutableHashSet.size(healthyRunners) === 0) {
        yield* Effect.logWarning("No healthy runners available");
        // to prevent a deadlock, we will mark the current node as healthy to
        // start the health check singleton again
        yield* withTimeout(runnerStorage.setRunnerHealth(selfRunner.address, true));
      }
      yield* Effect.sleep(config.refreshAssignmentsInterval);
    }
  }).pipe(Effect.catchAllCause(cause => Effect.logDebug(cause)), Effect.repeat(Schedule.spaced(1000)), Effect.annotateLogs({
    package: "@effect/cluster",
    module: "Sharding",
    fiber: "RunnerStorage sync",
    runner: config.runnerAddress
  }), Effect.forkIn(shardingScope), Effect.interruptible);
  const clientRequests = new Map();
  const clients = yield* _resourceMap.ResourceMap.make(Effect.fnUntraced(function* (entity) {
    const client = yield* RpcClient.makeNoSerialization(entity.protocol, {
      spanPrefix: `${entity.type}.client`,
      disableTracing: !Context.get(entity.protocol.annotations, ClusterSchema.ClientTracingEnabled),
      supportsAck: true,
      generateRequestId: () => (0, _RpcMessage.RequestId)(snowflakeGen.unsafeNext()),
      flatten: true,
      onFromClient(options) {
        const address = Context.unsafeGet(options.context, ClientAddressTag);
        switch (options.message._tag) {
          case "Request":
            {
              const fiber = Option.getOrThrow(Fiber.getCurrentFiber());
              const id = Snowflake.Snowflake(options.message.id);
              const rpc = entity.protocol.requests.get(options.message.tag);
              let respond;
              if (!options.discard) {
                const entry = {
                  rpc: rpc,
                  services: fiber.currentContext
                };
                clientRequests.set(id, entry);
                respond = makeClientRespond(entry, client.write);
              } else {
                respond = clientRespondDiscard;
              }
              return sendOutgoing(new Message.OutgoingRequest({
                envelope: Envelope.makeRequest({
                  requestId: id,
                  address,
                  tag: options.message.tag,
                  payload: options.message.payload,
                  headers: options.message.headers,
                  traceId: options.message.traceId,
                  spanId: options.message.spanId,
                  sampled: options.message.sampled
                }),
                lastReceivedReply: Option.none(),
                rpc,
                context: fiber.currentContext,
                respond
              }), options.discard);
            }
          case "Ack":
            {
              const requestId = Snowflake.Snowflake(options.message.requestId);
              const entry = clientRequests.get(requestId);
              if (!entry) return Effect.void;
              return sendOutgoing(new Message.OutgoingEnvelope({
                envelope: new Envelope.AckChunk({
                  id: snowflakeGen.unsafeNext(),
                  address,
                  requestId,
                  replyId: entry.lastChunkId
                }),
                rpc: entry.rpc
              }), false);
            }
          case "Interrupt":
            {
              const requestId = Snowflake.Snowflake(options.message.requestId);
              const entry = clientRequests.get(requestId);
              if (!entry) return Effect.void;
              clientRequests.delete(requestId);
              if (_ClusterSchema.Uninterruptible.forClient(entry.rpc.annotations)) {
                return Effect.void;
              }
              // for durable messages, we ignore interrupts on shutdown or as a
              // result of a shard being resassigned
              const isTransientInterrupt = MutableRef.get(isShutdown) || options.message.interruptors.some(id => _interruptors.internalInterruptors.has(id));
              if (isTransientInterrupt && Context.get(entry.rpc.annotations, _ClusterSchema.Persisted)) {
                return Effect.void;
              }
              return Effect.ignore(sendOutgoing(new Message.OutgoingEnvelope({
                envelope: new Envelope.Interrupt({
                  id: snowflakeGen.unsafeNext(),
                  address,
                  requestId
                }),
                rpc: entry.rpc
              }), false, 3));
            }
        }
        return Effect.void;
      }
    });
    yield* Scope.addFinalizer(yield* Effect.scope, Effect.fiberIdWith(fiberId => {
      _interruptors.internalInterruptors.add(fiberId);
      return Effect.void;
    }));
    return entityId => {
      const id = (0, _EntityId.make)(entityId);
      const address = ClientAddressTag.context((0, _EntityAddress.make)({
        shardId: getShardId(id, entity.getShardGroup(entityId)),
        entityId: id,
        entityType: entity.type
      }));
      const clientFn = function (tag, payload, options) {
        const context = options?.context ? Context.merge(options.context, address) : address;
        return client.client(tag, payload, {
          ...options,
          context
        });
      };
      const proxyClient = {};
      return new Proxy(proxyClient, {
        has(_, p) {
          return entity.protocol.requests.has(p);
        },
        get(target, p) {
          if (p in target) {
            return target[p];
          } else if (!entity.protocol.requests.has(p)) {
            return undefined;
          }
          return target[p] = (payload, options) => clientFn(p, payload, options);
        }
      });
    };
  }));
  const makeClient = entity => clients.get(entity);
  const clientRespondDiscard = _reply => Effect.void;
  const makeClientRespond = (entry, write) => reply => {
    switch (reply._tag) {
      case "Chunk":
        {
          entry.lastChunkId = reply.id;
          return write({
            _tag: "Chunk",
            clientId: 0,
            requestId: (0, _RpcMessage.RequestId)(reply.requestId),
            values: reply.values
          });
        }
      case "WithExit":
        {
          clientRequests.delete(reply.requestId);
          return write({
            _tag: "Exit",
            clientId: 0,
            requestId: (0, _RpcMessage.RequestId)(reply.requestId),
            exit: reply.exit
          });
        }
    }
  };
  // --- Singletons ---
  const singletons = new Map();
  const singletonFibers = yield* FiberMap.make();
  const withSingletonLock = Effect.unsafeMakeSemaphore(1).withPermits(1);
  const registerSingleton = Effect.fnUntraced(function* (name, run, options) {
    const shardGroup = options?.shardGroup ?? "default";
    const address = new _SingletonAddress.SingletonAddress({
      shardId: getShardId((0, _EntityId.make)(name), shardGroup),
      name
    });
    let map = singletons.get(address.shardId);
    if (!map) {
      map = MutableHashMap.empty();
      singletons.set(address.shardId, map);
    }
    if (MutableHashMap.has(map, address)) {
      return yield* Effect.die(`Singleton '${name}' is already registered`);
    }
    const context = yield* Effect.context();
    const wrappedRun = run.pipe(Effect.locally(FiberRef.currentLogAnnotations, HashMap.empty()), Effect.andThen(Effect.never), Effect.scoped, Effect.provide(context), Effect.orDie, Effect.interruptible);
    MutableHashMap.set(map, address, wrappedRun);
    yield* PubSub.publish(events, (0, _ShardingRegistrationEvent.SingletonRegistered)({
      address
    }));
    // start if we are on the right shard
    if (MutableHashSet.has(acquiredShards, address.shardId)) {
      yield* Effect.logDebug("Starting singleton", address);
      yield* FiberMap.run(singletonFibers, address, wrappedRun);
    }
    yield* Effect.addFinalizer(() => {
      const map = singletons.get(address.shardId);
      MutableHashMap.remove(map, address);
      return FiberMap.remove(singletonFibers, address);
    });
  }, withSingletonLock);
  const syncSingletons = withSingletonLock(Effect.gen(function* () {
    for (const [shardId, map] of singletons) {
      for (const [address, run] of map) {
        const running = FiberMap.unsafeHas(singletonFibers, address);
        const shouldBeRunning = MutableHashSet.has(acquiredShards, shardId);
        if (running && !shouldBeRunning) {
          yield* Effect.logDebug("Stopping singleton", address);
          _interruptors.internalInterruptors.add(Option.getOrThrow(Fiber.getCurrentFiber()).id());
          yield* FiberMap.remove(singletonFibers, address);
        } else if (!running && shouldBeRunning) {
          yield* Effect.logDebug("Starting singleton", address);
          yield* FiberMap.run(singletonFibers, address, run);
        }
      }
    }
    ClusterMetrics.singletons.unsafeUpdate(BigInt(yield* FiberMap.size(singletonFibers)), []);
  }));
  // --- Entities ---
  const context = yield* Effect.context();
  const reaper = yield* _entityReaper.EntityReaper;
  const entityManagerLatches = new Map();
  const registerEntity = Effect.fnUntraced(function* (entity, build, options) {
    if (Option.isNone(config.runnerAddress) || entityManagers.has(entity.type)) return;
    const scope = yield* Effect.scope;
    yield* Scope.addFinalizer(scope, Effect.sync(() => {
      state.status = "closed";
    }));
    const manager = yield* EntityManager.make(entity, build, {
      ...options,
      storage,
      runnerAddress: config.runnerAddress.value,
      sharding
    }).pipe(Effect.provide(context.pipe(Context.add(_entityReaper.EntityReaper, reaper), Context.add(Scope.Scope, scope), Context.add(Snowflake.Generator, snowflakeGen))));
    const state = {
      entity,
      status: "alive",
      manager
    };
    yield* Scope.addFinalizer(scope, Effect.fiberIdWith(id => {
      state.status = "closing";
      _interruptors.internalInterruptors.add(id);
      // if preemptive shutdown is enabled, we start shutting down Sharding
      // too
      return config.preemptiveShutdown ? shutdown() : Effect.void;
    }));
    // register entities while storage is idle
    // this ensures message order is preserved
    yield* withStorageReadLock(Effect.sync(() => {
      entityManagers.set(entity.type, state);
      if (entityManagerLatches.has(entity.type)) {
        entityManagerLatches.get(entity.type).unsafeOpen();
        entityManagerLatches.delete(entity.type);
      }
    }));
    yield* PubSub.publish(events, (0, _ShardingRegistrationEvent.EntityRegistered)({
      entity
    }));
  });
  const waitForEntityManager = entityType => {
    let latch = entityManagerLatches.get(entityType);
    if (!latch) {
      latch = Effect.unsafeMakeLatch();
      entityManagerLatches.set(entityType, latch);
    }
    return latch.await;
  };
  // --- Runner health checks ---
  if (selfRunner) {
    const checkRunner = ([runner, healthy]) => Effect.flatMap(runnerHealth.isAlive(runner.address), isAlive => {
      if (healthy === isAlive) return Effect.void;
      if (isAlive) {
        healthyRunnerCount++;
        return Effect.logDebug(`Runner is healthy`, runner).pipe(Effect.andThen(runnerStorage.setRunnerHealth(runner.address, isAlive)));
      }
      if (healthyRunnerCount <= 1) {
        // never mark the last runner as unhealthy, to prevent a deadlock
        return Effect.void;
      }
      healthyRunnerCount--;
      return Effect.logDebug(`Runner is unhealthy`, runner).pipe(Effect.andThen(runnerStorage.setRunnerHealth(runner.address, isAlive)));
    });
    yield* registerSingleton("effect/cluster/Sharding/RunnerHealth", Effect.gen(function* () {
      while (true) {
        // Skip health checks if we are the only runner
        if (MutableHashMap.size(allRunners) > 1) {
          yield* Effect.forEach(allRunners, checkRunner, {
            discard: true,
            concurrency: 10
          });
        }
        yield* Effect.sleep(config.runnerHealthCheckInterval);
      }
    }).pipe(Effect.catchAllCause(cause => Effect.logDebug("Runner health check failed", cause)), Effect.forever, Effect.annotateLogs({
      package: "@effect/cluster",
      module: "Sharding",
      fiber: "Runner health check"
    })));
  }
  // --- Finalization ---
  const shutdown = Effect.fnUntraced(function* (exit) {
    if (exit) {
      yield* Effect.logDebug("Shutting down", exit._tag === "Failure" ? exit.cause : {}).pipe(Effect.annotateLogs({
        package: "@effect/cluster",
        module: "Sharding"
      }));
    }
    _interruptors.internalInterruptors.add(yield* Effect.fiberId);
    if (isShutdown.current) return;
    MutableRef.set(isShutdown, true);
    if (selfRunner) {
      yield* Effect.ignore(runnerStorage.unregister(selfRunner.address));
    }
  });
  yield* Scope.addFinalizerExit(shardingScope, shutdown);
  const activeEntityCount = Effect.gen(function* () {
    let count = 0;
    for (const state of entityManagers.values()) {
      count += yield* state.manager.activeEntityCount;
    }
    return count;
  });
  const sharding = Sharding.of({
    getRegistrationEvents,
    getShardId,
    hasShardId(shardId) {
      if (isShutdown.current) return false;
      return MutableHashSet.has(acquiredShards, shardId);
    },
    getSnowflake: Effect.sync(() => snowflakeGen.unsafeNext()),
    isShutdown: Effect.sync(() => MutableRef.get(isShutdown)),
    registerEntity,
    registerSingleton,
    makeClient,
    send: sendLocal,
    sendOutgoing: (message, discard) => sendOutgoing(message, discard),
    notify: (message, options) => notifyLocal(message, false, options),
    activeEntityCount,
    pollStorage: storageReadLatch.open,
    reset
  });
  return sharding;
});
/**
 * @since 1.0.0
 * @category layers
 */
const layer = exports.layer = /*#__PURE__*/Layer.scoped(Sharding)(make).pipe(/*#__PURE__*/Layer.provide([Snowflake.layerGenerator, _entityReaper.EntityReaper.Default]));
// Utilities
const ClientAddressTag = /*#__PURE__*/Context.GenericTag("@effect/cluster/Sharding/ClientAddress");
//# sourceMappingURL=Sharding.js.map