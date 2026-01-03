/**
 * @since 1.0.0
 */
import { SocketServer } from "@effect/platform/SocketServer";
import type * as RpcSerialization from "@effect/rpc/RpcSerialization";
import * as Layer from "effect/Layer";
import type { MessageStorage } from "./MessageStorage.js";
import type { RunnerHealth } from "./RunnerHealth.js";
import type * as Runners from "./Runners.js";
import type * as RunnerStorage from "./RunnerStorage.js";
import type * as Sharding from "./Sharding.js";
import type { ShardingConfig } from "./ShardingConfig.js";
/**
 * @since 1.0.0
 * @category Layers
 */
export declare const layer: Layer.Layer<Sharding.Sharding | Runners.Runners, never, Runners.RpcClientProtocol | ShardingConfig | RpcSerialization.RpcSerialization | SocketServer | MessageStorage | RunnerStorage.RunnerStorage | RunnerHealth>;
/**
 * @since 1.0.0
 * @category Layers
 */
export declare const layerClientOnly: Layer.Layer<Sharding.Sharding | Runners.Runners, never, Runners.RpcClientProtocol | ShardingConfig | MessageStorage | RunnerStorage.RunnerStorage>;
//# sourceMappingURL=SocketRunner.d.ts.map