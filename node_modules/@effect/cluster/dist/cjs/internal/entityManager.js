"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.make = void 0;
var _RpcMessage = require("@effect/rpc/RpcMessage");
var RpcServer = _interopRequireWildcard(require("@effect/rpc/RpcServer"));
var Arr = _interopRequireWildcard(require("effect/Array"));
var Cause = _interopRequireWildcard(require("effect/Cause"));
var Context = _interopRequireWildcard(require("effect/Context"));
var Duration = _interopRequireWildcard(require("effect/Duration"));
var Effect = _interopRequireWildcard(require("effect/Effect"));
var Equal = _interopRequireWildcard(require("effect/Equal"));
var Exit = _interopRequireWildcard(require("effect/Exit"));
var FiberRef = _interopRequireWildcard(require("effect/FiberRef"));
var _Function = require("effect/Function");
var HashMap = _interopRequireWildcard(require("effect/HashMap"));
var Metric = _interopRequireWildcard(require("effect/Metric"));
var Option = _interopRequireWildcard(require("effect/Option"));
var ParseResult = _interopRequireWildcard(require("effect/ParseResult"));
var Runtime = _interopRequireWildcard(require("effect/Runtime"));
var Schedule = _interopRequireWildcard(require("effect/Schedule"));
var Schema = _interopRequireWildcard(require("effect/Schema"));
var Scope = _interopRequireWildcard(require("effect/Scope"));
var _ClusterError = require("../ClusterError.js");
var ClusterMetrics = _interopRequireWildcard(require("../ClusterMetrics.js"));
var _ClusterSchema = require("../ClusterSchema.js");
var _Entity = require("../Entity.js");
var Message = _interopRequireWildcard(require("../Message.js"));
var MessageStorage = _interopRequireWildcard(require("../MessageStorage.js"));
var Reply = _interopRequireWildcard(require("../Reply.js"));
var _ShardingConfig = require("../ShardingConfig.js");
var Snowflake = _interopRequireWildcard(require("../Snowflake.js"));
var _entityReaper = require("./entityReaper.js");
var _fiber = require("./fiber.js");
var _interruptors = require("./interruptors.js");
var _resourceMap = require("./resourceMap.js");
var _resourceRef = require("./resourceRef.js");
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/** @internal */
const make = exports.make = /*#__PURE__*/Effect.fnUntraced(function* (entity, buildHandlers, options) {
  const config = yield* _ShardingConfig.ShardingConfig;
  const snowflakeGen = yield* Snowflake.Generator;
  const managerScope = yield* Effect.scope;
  const storageEnabled = options.storage !== MessageStorage.noop;
  const mailboxCapacity = options.mailboxCapacity ?? config.entityMailboxCapacity;
  const clock = yield* Effect.clock;
  const context = yield* Effect.context();
  const retryDriver = yield* Schedule.driver(options.defectRetryPolicy ? Schedule.andThen(options.defectRetryPolicy, defaultRetryPolicy) : defaultRetryPolicy);
  const entityRpcs = new Map(entity.protocol.requests);
  // add internal rpcs
  entityRpcs.set(_Entity.KeepAliveRpc._tag, _Entity.KeepAliveRpc);
  const activeServers = new Map();
  const serverCloseLatches = new Map();
  const processedRequestIds = new Set();
  const entities = yield* _resourceMap.ResourceMap.make(Effect.fnUntraced(function* (address) {
    if (!options.sharding.hasShardId(address.shardId)) {
      return yield* new _ClusterError.EntityNotAssignedToRunner({
        address
      });
    }
    const scope = yield* Effect.scope;
    const endLatch = Effect.unsafeMakeLatch();
    const keepAliveLatch = Effect.unsafeMakeLatch(false);
    // on shutdown, reset the storage for the entity
    yield* Scope.addFinalizerExit(scope, () => {
      serverCloseLatches.get(address)?.unsafeOpen();
      serverCloseLatches.delete(address);
      return Effect.void;
    });
    const activeRequests = new Map();
    let defectRequestIds = [];
    // the server is stored in a ref, so if there is a defect, we can
    // swap the server without losing the active requests
    const writeRef = yield* _resourceRef.ResourceRef.from(scope, Effect.fnUntraced(function* (scope) {
      let isShuttingDown = false;
      // Initiate the behavior for the entity
      const handlers = yield* entity.protocol.toHandlersContext(buildHandlers).pipe(Effect.provide(context.pipe(Context.add(_Entity.CurrentAddress, address), Context.add(_Entity.CurrentRunnerAddress, options.runnerAddress), Context.add(_Entity.KeepAliveLatch, keepAliveLatch), Context.add(Scope.Scope, scope))), Effect.locally(FiberRef.currentLogAnnotations, HashMap.empty()));
      const server = yield* RpcServer.makeNoSerialization(entity.protocol, {
        spanPrefix: `${entity.type}(${address.entityId})`,
        spanAttributes: {
          ...options.spanAttributes,
          "entity.type": entity.type,
          "entity.id": address.entityId
        },
        concurrency: options.concurrency ?? 1,
        disableFatalDefects: options.disableFatalDefects,
        onFromServer(response) {
          switch (response._tag) {
            case "Exit":
              {
                const request = activeRequests.get(response.requestId);
                if (!request) return Effect.void;
                request.sentReply = true;
                // For durable messages, ignore interrupts during shutdown.
                // They will be retried when the entity is restarted.
                // Also, if the request is uninterruptible, we ignore the
                // interrupt.
                if (storageEnabled && Context.get(request.rpc.annotations, _ClusterSchema.Persisted) && Exit.isFailure(response.exit) && Exit.isInterrupted(response.exit) && (isShuttingDown || _ClusterSchema.Uninterruptible.forServer(request.rpc.annotations))) {
                  if (!isShuttingDown) {
                    return server.write(0, {
                      ...request.message.envelope,
                      id: (0, _RpcMessage.RequestId)(request.message.envelope.requestId),
                      tag: request.message.envelope.tag,
                      payload: new _Entity.Request({
                        ...request.message.envelope,
                        lastSentChunk: request.lastSentChunk
                      })
                    }).pipe(Effect.forkIn(scope));
                  }
                  activeRequests.delete(response.requestId);
                  return options.storage.unregisterReplyHandler(request.message.envelope.requestId);
                }
                return retryRespond(4, Effect.suspend(() => request.message.respond(new Reply.WithExit({
                  requestId: Snowflake.Snowflake(response.requestId),
                  id: snowflakeGen.unsafeNext(),
                  exit: response.exit
                })))).pipe(Effect.flatMap(() => {
                  processedRequestIds.add(request.message.envelope.requestId);
                  activeRequests.delete(response.requestId);
                  // ensure that the reaper does not remove the entity as we haven't
                  // been "idle" yet
                  if (activeRequests.size === 0) {
                    state.lastActiveCheck = clock.unsafeCurrentTimeMillis();
                  }
                  return Effect.void;
                }), Effect.orDie);
              }
            case "Chunk":
              {
                const request = activeRequests.get(response.requestId);
                if (!request) return Effect.void;
                const sequence = request.sequence;
                request.sequence++;
                if (!request.sentReply) {
                  request.sentReply = true;
                }
                return Effect.orDie(retryRespond(4, Effect.suspend(() => {
                  const reply = new Reply.Chunk({
                    requestId: Snowflake.Snowflake(response.requestId),
                    id: snowflakeGen.unsafeNext(),
                    sequence,
                    values: response.values
                  });
                  request.lastSentChunk = Option.some(reply);
                  return request.message.respond(reply);
                })));
              }
            case "Defect":
              {
                return Effect.forkIn(onDefect(Cause.die(response.defect)), managerScope);
              }
            case "ClientEnd":
              {
                return endLatch.open;
              }
          }
        }
      }).pipe(Scope.extend(scope), Effect.provide(handlers));
      yield* Scope.addFinalizer(scope, Effect.sync(() => {
        isShuttingDown = true;
      }));
      if (defectRequestIds.length > 0) {
        for (const id of defectRequestIds) {
          const {
            lastSentChunk,
            message
          } = activeRequests.get(id);
          yield* server.write(0, {
            ...message.envelope,
            id: (0, _RpcMessage.RequestId)(message.envelope.requestId),
            tag: message.envelope.tag,
            payload: new _Entity.Request({
              ...message.envelope,
              lastSentChunk
            })
          });
        }
        defectRequestIds = [];
      }
      return server.write;
    }));
    function onDefect(cause) {
      if (!activeServers.has(address.entityId)) {
        return endLatch.open;
      }
      const effect = writeRef.unsafeRebuild();
      defectRequestIds = Array.from(activeRequests.keys());
      return Effect.logError("Defect in entity, restarting", cause).pipe(Effect.andThen(Effect.ignore(retryDriver.next(void 0))), Effect.flatMap(() => activeServers.has(address.entityId) ? effect : endLatch.open), Effect.annotateLogs({
        module: "EntityManager",
        address,
        runner: options.runnerAddress
      }), Effect.catchAllCause(onDefect));
    }
    const state = {
      scope,
      address,
      write(clientId, message) {
        if (writeRef.state.current._tag !== "Acquired") {
          return Effect.flatMap(writeRef.await, write => write(clientId, message));
        }
        return writeRef.state.current.value(clientId, message);
      },
      activeRequests,
      lastActiveCheck: clock.unsafeCurrentTimeMillis(),
      keepAliveLatch,
      keepAliveEnabled: false
    };
    // During shutdown, signal that no more messages will be processed
    // and wait for the fiber to complete.
    //
    // If the termination timeout is reached, let the server clean itself up
    yield* Scope.addFinalizer(scope, Effect.withFiberRuntime(fiber => {
      activeServers.delete(address.entityId);
      serverCloseLatches.set(address, Effect.unsafeMakeLatch(false));
      _interruptors.internalInterruptors.add(fiber.id());
      return state.write(0, {
        _tag: "Eof"
      }).pipe(Effect.andThen(Effect.interruptible(endLatch.await)), Effect.timeoutOption(config.entityTerminationTimeout));
    }));
    activeServers.set(address.entityId, state);
    return state;
  }, Effect.locally(FiberRef.currentLogAnnotations, HashMap.empty())));
  const reaper = yield* _entityReaper.EntityReaper;
  const maxIdleTime = Duration.toMillis(options.maxIdleTime ?? config.entityMaxIdleTime);
  if (Number.isFinite(maxIdleTime)) {
    yield* reaper.register({
      maxIdleTime,
      servers: activeServers,
      entities
    });
  }
  // update metrics for active servers
  const gauge = ClusterMetrics.entities.pipe(Metric.tagged("type", entity.type));
  yield* Effect.sync(() => {
    gauge.unsafeUpdate(BigInt(activeServers.size), []);
  }).pipe(Effect.andThen(Effect.sleep(1000)), Effect.forever, Effect.forkIn(managerScope));
  function sendLocal(message) {
    return Effect.locally(Effect.flatMap(entities.get(message.envelope.address), server => {
      switch (message._tag) {
        case "IncomingRequestLocal":
          {
            // If the request is already running, then we might have more than
            // one sender for the same request. In this case, the other senders
            // should resume from storage only.
            let entry = server.activeRequests.get(message.envelope.requestId);
            if (entry || processedRequestIds.has(message.envelope.requestId)) {
              return Effect.fail(new _ClusterError.AlreadyProcessingMessage({
                envelopeId: message.envelope.requestId,
                address: message.envelope.address
              }));
            }
            const rpc = entityRpcs.get(message.envelope.tag);
            if (!storageEnabled && Context.get(rpc.annotations, _ClusterSchema.Persisted)) {
              return Effect.dieMessage("EntityManager.sendLocal: Cannot process a persisted message without MessageStorage");
            }
            // Cluster internal RPCs
            // keep-alive RPC
            if (rpc._tag === _Entity.KeepAliveRpc._tag) {
              const msg = message;
              const reply = Effect.suspend(() => Effect.orDie(retryRespond(4, msg.respond(new Reply.WithExit({
                requestId: message.envelope.requestId,
                id: snowflakeGen.unsafeNext(),
                exit: Exit.void
              })))));
              if (server.keepAliveEnabled) return reply;
              server.keepAliveEnabled = true;
              return server.keepAliveLatch.whenOpen(Effect.suspend(() => {
                server.keepAliveEnabled = false;
                return reply;
              })).pipe(Effect.forkIn(server.scope), Effect.asVoid);
            }
            if (mailboxCapacity !== "unbounded" && server.activeRequests.size >= mailboxCapacity) {
              return Effect.fail(new _ClusterError.MailboxFull({
                address: message.envelope.address
              }));
            }
            entry = {
              rpc,
              message,
              sentReply: false,
              lastSentChunk: message.lastSentReply,
              sequence: Option.match(message.lastSentReply, {
                onNone: () => 0,
                onSome: reply => reply._tag === "Chunk" ? reply.sequence + 1 : 0
              })
            };
            server.activeRequests.set(message.envelope.requestId, entry);
            return server.write(0, {
              ...message.envelope,
              id: (0, _RpcMessage.RequestId)(message.envelope.requestId),
              payload: new _Entity.Request({
                ...message.envelope,
                lastSentChunk: message.lastSentReply
              })
            });
          }
        case "IncomingEnvelope":
          {
            const entry = server.activeRequests.get(message.envelope.requestId);
            if (!entry) {
              return Effect.void;
            } else if (message.envelope._tag === "AckChunk" && Option.isSome(entry.lastSentChunk) && message.envelope.replyId !== entry.lastSentChunk.value.id) {
              return Effect.void;
            }
            return server.write(0, message.envelope._tag === "AckChunk" ? {
              _tag: "Ack",
              requestId: (0, _RpcMessage.RequestId)(message.envelope.requestId)
            } : {
              _tag: "Interrupt",
              requestId: (0, _RpcMessage.RequestId)(message.envelope.requestId),
              interruptors: []
            });
          }
      }
    }), FiberRef.currentLogAnnotations, HashMap.empty());
  }
  const decodeMessage = makeMessageDecode(entity, entityRpcs);
  const runFork = Runtime.runFork(yield* Effect.runtime().pipe(Effect.interruptible));
  return (0, _Function.identity)({
    interruptShard: shardId => Effect.suspend(function loop() {
      const fibers = Arr.empty();
      activeServers.forEach(state => {
        if (shardId[Equal.symbol](state.address.shardId)) {
          fibers.push(runFork(entities.removeIgnore(state.address)));
        }
      });
      serverCloseLatches.forEach((latch, address) => {
        if (shardId[Equal.symbol](address.shardId)) {
          fibers.push(runFork(latch.await));
        }
      });
      if (fibers.length === 0) return Effect.void;
      return Effect.flatMap((0, _fiber.joinAllDiscard)(fibers), loop);
    }),
    isProcessingFor(message, options) {
      if (options?.excludeReplies !== true && processedRequestIds.has(message.envelope.requestId)) {
        return true;
      }
      const state = activeServers.get(message.envelope.address.entityId);
      if (!state) return false;
      const request = state.activeRequests.get(message.envelope.requestId);
      if (request === undefined) {
        return false;
      } else if (options?.excludeReplies && request.sentReply) {
        return false;
      }
      return true;
    },
    clearProcessed() {
      processedRequestIds.clear();
    },
    sendLocal,
    send: message => decodeMessage(message).pipe(Effect.matchEffect({
      onFailure: cause => {
        if (message._tag === "IncomingEnvelope") {
          return Effect.die(new _ClusterError.MalformedMessage({
            cause
          }));
        }
        return Effect.orDie(message.respond(new Reply.ReplyWithContext({
          reply: new Reply.WithExit({
            id: snowflakeGen.unsafeNext(),
            requestId: message.envelope.requestId,
            exit: Exit.die(new _ClusterError.MalformedMessage({
              cause
            }))
          }),
          rpc: entityRpcs.get(message.envelope.tag),
          context
        })));
      },
      onSuccess: decoded => {
        if (decoded._tag === "IncomingEnvelope") {
          return sendLocal(new Message.IncomingEnvelope(decoded));
        }
        const request = message;
        const rpc = entityRpcs.get(decoded.envelope.tag);
        return sendLocal(new Message.IncomingRequestLocal({
          envelope: decoded.envelope,
          lastSentReply: decoded.lastSentReply,
          respond: reply => request.respond(new Reply.ReplyWithContext({
            reply,
            rpc,
            context
          }))
        }));
      }
    }), Effect.provide(context)),
    activeEntityCount: Effect.sync(() => activeServers.size)
  });
});
const defaultRetryPolicy = /*#__PURE__*/Schedule.exponential(500, 1.5).pipe(/*#__PURE__*/Schedule.union(/*#__PURE__*/Schedule.spaced("10 seconds")));
const makeMessageDecode = (entity, entityRpcs) => {
  const decodeRequest = (message, rpc) => {
    const payload = Schema.decode(rpc.payloadSchema)(message.envelope.payload);
    const lastSentReply = Option.isSome(message.lastSentReply) ? Effect.asSome(Schema.decode(Reply.Reply(rpc))(message.lastSentReply.value)) : Effect.succeedNone;
    return Effect.flatMap(payload, payload => Effect.map(lastSentReply, lastSentReply => ({
      _tag: "IncomingRequest",
      envelope: {
        ...message.envelope,
        payload
      },
      lastSentReply
    })));
  };
  return message => {
    if (message._tag === "IncomingEnvelope") {
      return Effect.succeed(message);
    }
    const rpc = entityRpcs.get(message.envelope.tag);
    if (!rpc) {
      return Effect.fail(new ParseResult.ParseError({
        issue: new ParseResult.Unexpected(message, `Unknown tag ${message.envelope.tag} for entity type ${entity.type}`)
      }));
    }
    return decodeRequest(message, rpc);
  };
};
const retryRespond = (times, effect) => times === 0 ? effect : Effect.catchAll(effect, () => Effect.delay(retryRespond(times - 1, effect), 200));
//# sourceMappingURL=entityManager.js.map