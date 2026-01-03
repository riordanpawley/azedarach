/**
 * @since 1.0.0
 */
import * as HttpClient from "@effect/platform/HttpClient";
import * as HttpRouter from "@effect/platform/HttpLayerRouter";
import type * as HttpServer from "@effect/platform/HttpServer";
import type { HttpServerRequest } from "@effect/platform/HttpServerRequest";
import type { HttpServerResponse } from "@effect/platform/HttpServerResponse";
import * as Socket from "@effect/platform/Socket";
import * as RpcSerialization from "@effect/rpc/RpcSerialization";
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import type { Scope } from "effect/Scope";
import type { MessageStorage } from "./MessageStorage.js";
import type { RunnerHealth } from "./RunnerHealth.js";
import * as Runners from "./Runners.js";
import { RpcClientProtocol } from "./Runners.js";
import type { RunnerStorage } from "./RunnerStorage.js";
import * as Sharding from "./Sharding.js";
import type * as ShardingConfig from "./ShardingConfig.js";
/**
 * @since 1.0.0
 * @category Layers
 */
export declare const layerClientProtocolHttp: (options: {
    readonly path: string;
    readonly https?: boolean | undefined;
}) => Layer.Layer<RpcClientProtocol, never, RpcSerialization.RpcSerialization | HttpClient.HttpClient>;
/**
 * @since 1.0.0
 * @category Layers
 */
export declare const layerClientProtocolHttpDefault: Layer.Layer<Runners.RpcClientProtocol, never, RpcSerialization.RpcSerialization | HttpClient.HttpClient>;
/**
 * @since 1.0.0
 * @category Layers
 */
export declare const layerClientProtocolWebsocket: (options: {
    readonly path: string;
    readonly https?: boolean | undefined;
}) => Layer.Layer<RpcClientProtocol, never, RpcSerialization.RpcSerialization | Socket.WebSocketConstructor>;
/**
 * @since 1.0.0
 * @category Layers
 */
export declare const layerClientProtocolWebsocketDefault: Layer.Layer<Runners.RpcClientProtocol, never, RpcSerialization.RpcSerialization | Socket.WebSocketConstructor>;
/**
 * @since 1.0.0
 * @category Http App
 */
export declare const toHttpEffect: Effect.Effect<Effect.Effect<HttpServerResponse, never, Scope | HttpServerRequest>, never, Scope | RpcSerialization.RpcSerialization | Sharding.Sharding | MessageStorage>;
/**
 * @since 1.0.0
 * @category Http App
 */
export declare const toHttpEffectWebsocket: Effect.Effect<Effect.Effect<HttpServerResponse, never, Scope | HttpServerRequest>, never, Scope | RpcSerialization.RpcSerialization | Sharding.Sharding | MessageStorage>;
/**
 * @since 1.0.0
 * @category Layers
 */
export declare const layerClient: Layer.Layer<Sharding.Sharding | Runners.Runners, never, ShardingConfig.ShardingConfig | Runners.RpcClientProtocol | MessageStorage | RunnerStorage | RunnerHealth>;
/**
 * A HTTP layer for the `Runners` services, that adds a route to the provided
 * `HttpRouter`.
 *
 * @since 1.0.0
 * @category Layers
 */
export declare const layerHttpOptions: (options: {
    readonly path: HttpRouter.PathInput;
}) => Layer.Layer<Sharding.Sharding | Runners.Runners, never, RunnerStorage | RunnerHealth | RpcSerialization.RpcSerialization | MessageStorage | ShardingConfig.ShardingConfig | Runners.RpcClientProtocol | HttpRouter.HttpRouter>;
/**
 * @since 1.0.0
 * @category Layers
 */
export declare const layerWebsocketOptions: (options: {
    readonly path: HttpRouter.PathInput;
}) => Layer.Layer<Sharding.Sharding | Runners.Runners, never, ShardingConfig.ShardingConfig | Runners.RpcClientProtocol | MessageStorage | RunnerStorage | RunnerHealth | RpcSerialization.RpcSerialization | HttpRouter.HttpRouter>;
/**
 * @since 1.0.0
 * @category Layers
 */
export declare const layerHttp: Layer.Layer<Sharding.Sharding | Runners.Runners, never, RpcSerialization.RpcSerialization | ShardingConfig.ShardingConfig | HttpClient.HttpClient | HttpServer.HttpServer | MessageStorage | RunnerStorage | RunnerHealth>;
/**
 * @since 1.0.0
 * @category Layers
 */
export declare const layerHttpClientOnly: Layer.Layer<Sharding.Sharding | Runners.Runners, never, RpcSerialization.RpcSerialization | ShardingConfig.ShardingConfig | HttpClient.HttpClient | MessageStorage | RunnerStorage>;
/**
 * @since 1.0.0
 * @category Layers
 */
export declare const layerWebsocket: Layer.Layer<Sharding.Sharding | Runners.Runners, never, RpcSerialization.RpcSerialization | ShardingConfig.ShardingConfig | Socket.WebSocketConstructor | HttpServer.HttpServer | MessageStorage | RunnerStorage | RunnerHealth>;
/**
 * @since 1.0.0
 * @category Layers
 */
export declare const layerWebsocketClientOnly: Layer.Layer<Sharding.Sharding | Runners.Runners, never, ShardingConfig.ShardingConfig | MessageStorage | RunnerStorage | RpcSerialization.RpcSerialization | Socket.WebSocketConstructor>;
//# sourceMappingURL=HttpRunner.d.ts.map