/**
 * @since 1.0.0
 */
import * as SqlClient from "@effect/sql/SqlClient";
import type { SqlError } from "@effect/sql/SqlError";
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import * as Scope from "effect/Scope";
import { PersistenceError } from "./ClusterError.js";
import * as RunnerStorage from "./RunnerStorage.js";
import * as ShardId from "./ShardId.js";
import * as ShardingConfig from "./ShardingConfig.js";
/**
 * @since 1.0.0
 * @category Constructors
 */
export declare const make: (options: {
    readonly prefix?: string | undefined;
}) => Effect.Effect<{
    readonly register: (runner: import("./Runner.js").Runner, healthy: boolean) => Effect.Effect<import("./MachineId.js").MachineId, PersistenceError>;
    readonly unregister: (address: import("./RunnerAddress.js").RunnerAddress) => Effect.Effect<void, PersistenceError>;
    readonly getRunners: Effect.Effect<Array<readonly [runner: import("./Runner.js").Runner, healthy: boolean]>, PersistenceError>;
    readonly setRunnerHealth: (address: import("./RunnerAddress.js").RunnerAddress, healthy: boolean) => Effect.Effect<void, PersistenceError>;
    readonly acquire: (address: import("./RunnerAddress.js").RunnerAddress, shardIds: Iterable<ShardId.ShardId>) => Effect.Effect<Array<ShardId.ShardId>, PersistenceError>;
    readonly refresh: (address: import("./RunnerAddress.js").RunnerAddress, shardIds: Iterable<ShardId.ShardId>) => Effect.Effect<Array<ShardId.ShardId>, PersistenceError>;
    readonly release: (address: import("./RunnerAddress.js").RunnerAddress, shardId: ShardId.ShardId) => Effect.Effect<void, PersistenceError>;
    readonly releaseAll: (address: import("./RunnerAddress.js").RunnerAddress) => Effect.Effect<void, PersistenceError>;
}, SqlError, Scope.Scope | ShardingConfig.ShardingConfig | SqlClient.SqlClient>;
/**
 * @since 1.0.0
 * @category Layers
 */
export declare const layer: Layer.Layer<RunnerStorage.RunnerStorage, SqlError, SqlClient.SqlClient | ShardingConfig.ShardingConfig>;
/**
 * @since 1.0.0
 * @category Layers
 */
export declare const layerWith: (options: {
    readonly prefix?: string | undefined;
}) => Layer.Layer<RunnerStorage.RunnerStorage, SqlError, SqlClient.SqlClient | ShardingConfig.ShardingConfig>;
//# sourceMappingURL=SqlRunnerStorage.d.ts.map