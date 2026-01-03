/**
 * @since 1.0.0
 */
import * as Runners from "@effect/cluster/Runners";
import * as ShardingConfig from "@effect/cluster/ShardingConfig";
import type * as SocketServer from "@effect/platform/SocketServer";
import * as RpcSerialization from "@effect/rpc/RpcSerialization";
import * as Layer from "effect/Layer";
/**
 * @since 1.0.0
 * @category Layers
 */
export declare const layerClientProtocol: Layer.Layer<Runners.RpcClientProtocol, never, RpcSerialization.RpcSerialization>;
/**
 * @since 1.0.0
 * @category Layers
 */
export declare const layerSocketServer: Layer.Layer<SocketServer.SocketServer, SocketServer.SocketServerError, ShardingConfig.ShardingConfig>;
//# sourceMappingURL=NodeClusterSocket.d.ts.map