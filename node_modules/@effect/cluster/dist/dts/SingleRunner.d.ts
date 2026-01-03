/**
 * @since 1.0.0
 */
import type * as SqlClient from "@effect/sql/SqlClient";
import type * as ConfigError from "effect/ConfigError";
import * as Layer from "effect/Layer";
import type * as MessageStorage from "./MessageStorage.js";
import * as Runners from "./Runners.js";
import * as Sharding from "./Sharding.js";
import * as ShardingConfig from "./ShardingConfig.js";
/**
 * A sql backed single-node cluster, that can be used for running durable
 * entities and workflows.
 *
 * @since 1.0.0
 * @category Layers
 */
export declare const layer: (options?: {
    readonly shardingConfig?: Partial<ShardingConfig.ShardingConfig["Type"]> | undefined;
    readonly runnerStorage?: "memory" | "sql" | undefined;
}) => Layer.Layer<Sharding.Sharding | Runners.Runners | MessageStorage.MessageStorage, ConfigError.ConfigError, SqlClient.SqlClient>;
//# sourceMappingURL=SingleRunner.d.ts.map