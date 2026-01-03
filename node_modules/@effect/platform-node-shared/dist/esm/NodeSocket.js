/**
 * @since 1.0.0
 */
import * as Socket from "@effect/platform/Socket";
import * as Channel from "effect/Channel";
import * as Context from "effect/Context";
import * as Deferred from "effect/Deferred";
import * as Effect from "effect/Effect";
import * as FiberSet from "effect/FiberSet";
import { identity } from "effect/Function";
import * as Layer from "effect/Layer";
import * as Scope from "effect/Scope";
import * as Net from "node:net";
/**
 * @since 1.0.0
 * @category tags
 */
export const NetSocket = /*#__PURE__*/Context.GenericTag("@effect/platform-node/NodeSocket/NetSocket");
/**
 * @since 1.0.0
 * @category constructors
 */
export const makeNet = options => fromDuplex(Effect.scopeWith(scope => {
  let conn;
  return Effect.flatMap(Scope.addFinalizer(scope, Effect.sync(() => {
    if (!conn) return;
    if (conn.closed === false) {
      if ("destroySoon" in conn) {
        conn.destroySoon();
      } else {
        ;
        conn.destroy();
      }
    }
  })), () => Effect.async(resume => {
    conn = Net.createConnection(options);
    conn.once("connect", () => {
      resume(Effect.succeed(conn));
    });
    conn.on("error", cause => {
      resume(Effect.fail(new Socket.SocketGenericError({
        reason: "Open",
        cause
      })));
    });
  }));
}), options);
/**
 * @since 1.0.0
 * @category constructors
 */
export const fromDuplex = (open, options) => Effect.withFiberRuntime(fiber => {
  let currentSocket;
  const latch = Effect.unsafeMakeLatch(false);
  const openContext = fiber.currentContext;
  const run = (handler, opts) => Effect.scopedWith(Effect.fnUntraced(function* (scope) {
    const fiberSet = yield* FiberSet.make().pipe(Scope.extend(scope));
    let conn = undefined;
    yield* Scope.addFinalizer(scope, Effect.sync(() => {
      if (!conn) return;
      conn.off("data", onData);
      conn.off("end", onEnd);
      conn.off("error", onError);
      conn.off("close", onClose);
    }));
    conn = yield* Scope.extend(open, scope).pipe(options?.openTimeout ? Effect.timeoutFail({
      duration: options.openTimeout,
      onTimeout: () => new Socket.SocketGenericError({
        reason: "Open",
        cause: new Error("Connection timed out")
      })
    }) : identity);
    conn.on("end", onEnd);
    conn.on("error", onError);
    conn.on("close", onClose);
    const run = yield* Effect.provideService(FiberSet.runtime(fiberSet)(), NetSocket, conn);
    conn.on("data", onData);
    currentSocket = conn;
    yield* latch.open;
    if (opts?.onOpen) yield* opts.onOpen;
    return yield* FiberSet.join(fiberSet);
    function onData(chunk) {
      const result = handler(chunk);
      if (Effect.isEffect(result)) {
        run(result);
      }
    }
    function onEnd() {
      Deferred.unsafeDone(fiberSet.deferred, Effect.void);
    }
    function onError(cause) {
      Deferred.unsafeDone(fiberSet.deferred, Effect.fail(new Socket.SocketGenericError({
        reason: "Read",
        cause
      })));
    }
    function onClose(hadError) {
      Deferred.unsafeDone(fiberSet.deferred, Effect.fail(new Socket.SocketCloseError({
        reason: "Close",
        code: hadError ? 1006 : 1000
      })));
    }
  })).pipe(Effect.mapInputContext(input => Context.merge(openContext, input)), Effect.ensuring(Effect.sync(() => {
    latch.unsafeClose();
    currentSocket = undefined;
  })), Effect.interruptible);
  const write = chunk => latch.whenOpen(Effect.async(resume => {
    const conn = currentSocket;
    if (Socket.isCloseEvent(chunk)) {
      conn.destroy(chunk.code > 1000 ? new Error(`closed with code ${chunk.code}`) : undefined);
      return resume(Effect.void);
    }
    currentSocket.write(chunk, cause => {
      resume(cause ? Effect.fail(new Socket.SocketGenericError({
        reason: "Write",
        cause
      })) : Effect.void);
    });
  }));
  const writer = Effect.acquireRelease(Effect.succeed(write), () => Effect.sync(() => {
    if (!currentSocket || currentSocket.writableEnded) return;
    currentSocket.end();
  }));
  return Effect.succeed(Socket.Socket.of({
    [Socket.TypeId]: Socket.TypeId,
    run,
    runRaw: run,
    writer
  }));
});
/**
 * @since 1.0.0
 * @category constructors
 */
export const makeNetChannel = options => Channel.unwrapScoped(Effect.map(makeNet(options), Socket.toChannelWith()));
/**
 * @since 1.0.0
 * @category layers
 */
export const layerNet = options => Layer.effect(Socket.Socket, makeNet(options));
//# sourceMappingURL=NodeSocket.js.map