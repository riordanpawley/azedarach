"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.makeWebSocket = exports.make = exports.layerWebSocket = exports.layer = exports.IncomingMessage = void 0;
var Socket = _interopRequireWildcard(require("@effect/platform/Socket"));
var SocketServer = _interopRequireWildcard(require("@effect/platform/SocketServer"));
var Context = _interopRequireWildcard(require("effect/Context"));
var Deferred = _interopRequireWildcard(require("effect/Deferred"));
var Effect = _interopRequireWildcard(require("effect/Effect"));
var Exit = _interopRequireWildcard(require("effect/Exit"));
var FiberRef = _interopRequireWildcard(require("effect/FiberRef"));
var FiberSet = _interopRequireWildcard(require("effect/FiberSet"));
var _Function = require("effect/Function");
var Layer = _interopRequireWildcard(require("effect/Layer"));
var Scope = _interopRequireWildcard(require("effect/Scope"));
var Net = _interopRequireWildcard(require("node:net"));
var WS = _interopRequireWildcard(require("ws"));
var NodeSocket = _interopRequireWildcard(require("./NodeSocket.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 */

/**
 * @since 1.0.0
 * @category tags
 */
class IncomingMessage extends /*#__PURE__*/Context.Tag("@effect/platform-node-shared/NodeSocketServer/IncomingMessage")() {}
/**
 * @since 1.0.0
 * @category constructors
 */
exports.IncomingMessage = IncomingMessage;
const make = exports.make = /*#__PURE__*/Effect.fnUntraced(function* (options) {
  const errorDeferred = yield* Deferred.make();
  const pending = [];
  const defaultOnConnection = socket => {
    pending.push(socket);
  };
  let onConnection = defaultOnConnection;
  yield* Effect.addFinalizer(() => Effect.async(resume => {
    server.close(() => resume(Effect.void));
  }));
  const server = Net.createServer(options, conn => onConnection(conn));
  server.on("error", cause => Deferred.unsafeDone(errorDeferred, Exit.fail(cause)));
  yield* Effect.async(resume => {
    server.listen(options, () => {
      resume(Effect.void);
    });
  }).pipe(Effect.raceFirst(Effect.mapError(Deferred.await(errorDeferred), cause => new SocketServer.SocketServerError({
    reason: "Open",
    cause
  }))));
  const run = Effect.fnUntraced(function* (handler) {
    const scope = yield* Scope.make();
    const fiberSet = yield* FiberSet.make().pipe(Scope.extend(scope));
    const run = yield* FiberSet.runtime(fiberSet)();
    function onConnection_(conn) {
      let error;
      conn.on("error", err => {
        error = err;
      });
      (0, _Function.pipe)(NodeSocket.fromDuplex(Effect.acquireRelease(Effect.suspend(() => {
        if (error) {
          return Effect.fail(new Socket.SocketGenericError({
            reason: "Open",
            cause: error
          }));
        } else if (conn.closed) {
          return Effect.fail(new Socket.SocketCloseError({
            reason: "Close",
            code: 1000
          }));
        }
        return Effect.succeed(conn);
      }), conn => Effect.sync(() => {
        if (conn.closed === false) {
          conn.destroySoon();
        }
      }))), Effect.flatMap(handler), Effect.catchAllCause(reportUnhandledError), Effect.provideService(NodeSocket.NetSocket, conn), run);
    }
    return yield* Effect.async(_resume => {
      const prev = onConnection;
      onConnection = onConnection_;
      pending.forEach(onConnection);
      pending.length = 0;
      return Effect.suspend(() => {
        onConnection = prev;
        return Scope.close(scope, Exit.void);
      });
    }).pipe(Effect.raceFirst(Effect.mapError(Deferred.await(errorDeferred), cause => new SocketServer.SocketServerError({
      reason: "Unknown",
      cause
    }))));
  });
  const address = server.address();
  return SocketServer.SocketServer.of({
    address: typeof address === "string" ? {
      _tag: "UnixAddress",
      path: address
    } : {
      _tag: "TcpAddress",
      hostname: address.address,
      port: address.port
    },
    run
  });
});
/**
 * @since 1.0.0
 * @category layers
 */
const layer = options => Layer.scoped(SocketServer.SocketServer, make(options));
/**
 * @since 1.0.0
 * @category constructors
 */
exports.layer = layer;
const makeWebSocket = exports.makeWebSocket = /*#__PURE__*/Effect.fnUntraced(function* (options) {
  const server = yield* Effect.acquireRelease(Effect.sync(() => new WS.WebSocketServer(options)), server => Effect.async(resume => {
    server.close(() => resume(Effect.void));
  }));
  yield* Effect.async(resume => {
    server.once("error", error => {
      resume(Effect.fail(new SocketServer.SocketServerError({
        reason: "Open",
        cause: error
      })));
    });
    server.once("listening", () => {
      resume(Effect.void);
    });
  });
  const run = Effect.fnUntraced(function* (handler) {
    const scope = yield* Scope.make();
    const fiberSet = yield* FiberSet.make().pipe(Scope.extend(scope));
    const run = yield* FiberSet.runtime(fiberSet)();
    function onConnection(conn, req) {
      (0, _Function.pipe)(Socket.fromWebSocket(Effect.acquireRelease(Effect.succeed(conn), conn => Effect.sync(() => {
        conn.close();
      }))), Effect.flatMap(handler), Effect.catchAllCause(reportUnhandledError), Effect.provideService(Socket.WebSocket, conn), Effect.provideService(IncomingMessage, req), run);
    }
    return yield* Effect.async(_resume => {
      server.on("connection", onConnection);
      return Effect.sync(() => {
        server.off("connection", onConnection);
      });
    }).pipe(Effect.ensuring(Scope.close(scope, Exit.void)));
  });
  const address = server.address();
  return SocketServer.SocketServer.of({
    address: typeof address === "string" ? {
      _tag: "UnixAddress",
      path: address
    } : {
      _tag: "TcpAddress",
      hostname: address.address,
      port: address.port
    },
    run
  });
});
/**
 * @since 1.0.0
 * @category layers
 */
const layerWebSocket = options => Layer.scoped(SocketServer.SocketServer, makeWebSocket(options));
exports.layerWebSocket = layerWebSocket;
const reportUnhandledError = cause => Effect.withFiberRuntime(fiber => {
  const unhandledLogLevel = fiber.getFiberRef(FiberRef.unhandledErrorLogLevel);
  if (unhandledLogLevel._tag === "Some") {
    return Effect.logWithLevel(unhandledLogLevel.value, cause, "Unhandled error in SocketServer");
  }
  return Effect.void;
});
//# sourceMappingURL=NodeSocketServer.js.map