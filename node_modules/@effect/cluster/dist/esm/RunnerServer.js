/**
 * @since 1.0.0
 */
import * as RpcServer from "@effect/rpc/RpcServer";
import * as Effect from "effect/Effect";
import * as Fiber from "effect/Fiber";
import { constant } from "effect/Function";
import * as Layer from "effect/Layer";
import * as Mailbox from "effect/Mailbox";
import * as Option from "effect/Option";
import * as Runtime from "effect/Runtime";
import * as Message from "./Message.js";
import * as MessageStorage from "./MessageStorage.js";
import * as Reply from "./Reply.js";
import * as RunnerHealth from "./RunnerHealth.js";
import * as Runners from "./Runners.js";
import * as Sharding from "./Sharding.js";
import { ShardingConfig } from "./ShardingConfig.js";
const constVoid = /*#__PURE__*/constant(Effect.void);
/**
 * @since 1.0.0
 * @category Layers
 */
export const layerHandlers = /*#__PURE__*/Runners.Rpcs.toLayer(/*#__PURE__*/Effect.gen(function* () {
  const sharding = yield* Sharding.Sharding;
  const storage = yield* MessageStorage.MessageStorage;
  return {
    Ping: () => Effect.void,
    Notify: ({
      envelope
    }) => sharding.notify(envelope._tag === "Request" ? new Message.IncomingRequest({
      envelope,
      respond: constVoid,
      lastSentReply: Option.none()
    }) : new Message.IncomingEnvelope({
      envelope
    })),
    Effect: ({
      persisted,
      request
    }) => {
      let replyEncoded = undefined;
      let resume = reply => {
        replyEncoded = reply;
      };
      const message = new Message.IncomingRequest({
        envelope: request,
        lastSentReply: Option.none(),
        respond(reply) {
          resume(Effect.orDie(Reply.serialize(reply)));
          return Effect.void;
        }
      });
      if (persisted) {
        return Effect.async(resume_ => {
          resume = resume_;
          const parent = Option.getOrThrow(Fiber.getCurrentFiber());
          const runtime = Runtime.make({
            context: parent.currentContext,
            runtimeFlags: Runtime.defaultRuntimeFlags,
            fiberRefs: parent.getFiberRefs()
          });
          const onExit = exit => {
            if (exit._tag === "Failure") {
              resume(exit);
            }
          };
          const fiber = Runtime.runFork(runtime)(storage.registerReplyHandler(message));
          fiber.addObserver(onExit);
          Runtime.runFork(runtime)(Effect.catchTag(sharding.notify(message, constWaitUntilRead), "AlreadyProcessingMessage", () => Effect.void)).addObserver(onExit);
          return Fiber.interrupt(fiber);
        });
      }
      return Effect.zipRight(sharding.send(message), Effect.async(resume_ => {
        if (replyEncoded) {
          resume_(replyEncoded);
        } else {
          resume = resume_;
        }
      }));
    },
    Stream: ({
      persisted,
      request
    }) => Effect.flatMap(Mailbox.make(), mailbox => {
      const message = new Message.IncomingRequest({
        envelope: request,
        lastSentReply: Option.none(),
        respond(reply) {
          return Effect.flatMap(Reply.serialize(reply), reply => {
            mailbox.unsafeOffer(reply);
            return Effect.void;
          });
        }
      });
      return Effect.as(persisted ? Effect.zipRight(storage.registerReplyHandler(message).pipe(Effect.onError(cause => mailbox.failCause(cause)), Effect.forkScoped, Effect.interruptible), sharding.notify(message, constWaitUntilRead)) : sharding.send(message), mailbox);
    }),
    Envelope: ({
      envelope
    }) => sharding.send(new Message.IncomingEnvelope({
      envelope
    }))
  };
}));
const constWaitUntilRead = {
  waitUntilRead: true
};
/**
 * The `RunnerServer` recieves messages from other Runners and forwards them to the
 * `Sharding` layer.
 *
 * It also responds to `Ping` requests.
 *
 * @since 1.0.0
 * @category Layers
 */
export const layer = /*#__PURE__*/RpcServer.layer(Runners.Rpcs, {
  spanPrefix: "RunnerServer",
  disableTracing: true
}).pipe(/*#__PURE__*/Layer.provide(layerHandlers));
/**
 * A `RunnerServer` layer that includes the `Runners` & `Sharding` clients.
 *
 * @since 1.0.0
 * @category Layers
 */
export const layerWithClients = /*#__PURE__*/layer.pipe(/*#__PURE__*/Layer.provideMerge(Sharding.layer), /*#__PURE__*/Layer.provideMerge(Runners.layerRpc));
/**
 * A `Runners` layer that is client only.
 *
 * It will not register with RunnerStorage and recieve shard assignments,
 * so this layer can be used to embed a cluster client inside another effect
 * application.
 *
 * @since 1.0.0
 * @category Layers
 */
export const layerClientOnly = /*#__PURE__*/Sharding.layer.pipe(/*#__PURE__*/Layer.provideMerge(Runners.layerRpc), /*#__PURE__*/Layer.provide(RunnerHealth.layerNoop), /*#__PURE__*/Layer.updateService(ShardingConfig, config => ({
  ...config,
  runnerAddress: Option.none()
})));
//# sourceMappingURL=RunnerServer.js.map