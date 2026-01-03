/**
 * @since 1.0.0
 */
import * as Socket from "@effect/platform/Socket";
import * as SocketServer from "@effect/platform/SocketServer";
import * as Context from "effect/Context";
import * as Deferred from "effect/Deferred";
import * as Effect from "effect/Effect";
import * as Exit from "effect/Exit";
import * as FiberRef from "effect/FiberRef";
import * as FiberSet from "effect/FiberSet";
import { pipe } from "effect/Function";
import * as Layer from "effect/Layer";
import * as Scope from "effect/Scope";
import * as Net from "node:net";
import * as WS from "ws";
import * as NodeSocket from "./NodeSocket.js";
/**
 * @since 1.0.0
 * @category tags
 */
export class IncomingMessage extends /*#__PURE__*/Context.Tag("@effect/platform-node-shared/NodeSocketServer/IncomingMessage")() {}
/**
 * @since 1.0.0
 * @category constructors
 */
export const make = /*#__PURE__*/Effect.fnUntraced(function* (options) {
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
      pipe(NodeSocket.fromDuplex(Effect.acquireRelease(Effect.suspend(() => {
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
export const layer = options => Layer.scoped(SocketServer.SocketServer, make(options));
/**
 * @since 1.0.0
 * @category constructors
 */
export const makeWebSocket = /*#__PURE__*/Effect.fnUntraced(function* (options) {
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
      pipe(Socket.fromWebSocket(Effect.acquireRelease(Effect.succeed(conn), conn => Effect.sync(() => {
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
export const layerWebSocket = options => Layer.scoped(SocketServer.SocketServer, makeWebSocket(options));
const reportUnhandledError = cause => Effect.withFiberRuntime(fiber => {
  const unhandledLogLevel = fiber.getFiberRef(FiberRef.unhandledErrorLogLevel);
  if (unhandledLogLevel._tag === "Some") {
    return Effect.logWithLevel(unhandledLogLevel.value, cause, "Unhandled error in SocketServer");
  }
  return Effect.void;
});
//# sourceMappingURL=NodeSocketServer.js.map