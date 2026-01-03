/**
 * @since 1.0.0
 */
import { type NonEmptyArray } from "effect/Array";
import * as Context from "effect/Context";
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import type { PersistenceError } from "./ClusterError.js";
import * as MachineId from "./MachineId.js";
import { Runner } from "./Runner.js";
import type { RunnerAddress } from "./RunnerAddress.js";
import { ShardId } from "./ShardId.js";
declare const RunnerStorage_base: Context.TagClass<RunnerStorage, "@effect/cluster/RunnerStorage", {
    /**
     * Register a new runner with the cluster.
     */
    readonly register: (runner: Runner, healthy: boolean) => Effect.Effect<MachineId.MachineId, PersistenceError>;
    /**
     * Unregister the runner with the given address.
     */
    readonly unregister: (address: RunnerAddress) => Effect.Effect<void, PersistenceError>;
    /**
     * Get all runners registered with the cluster.
     */
    readonly getRunners: Effect.Effect<Array<readonly [runner: Runner, healthy: boolean]>, PersistenceError>;
    /**
     * Set the health status of the given runner.
     */
    readonly setRunnerHealth: (address: RunnerAddress, healthy: boolean) => Effect.Effect<void, PersistenceError>;
    /**
     * Try to acquire the given shard ids for processing.
     *
     * It returns an array of shards it was able to acquire.
     */
    readonly acquire: (address: RunnerAddress, shardIds: Iterable<ShardId>) => Effect.Effect<Array<ShardId>, PersistenceError>;
    /**
     * Refresh the locks owned by the given runner.
     */
    readonly refresh: (address: RunnerAddress, shardIds: Iterable<ShardId>) => Effect.Effect<Array<ShardId>, PersistenceError>;
    /**
     * Release the given shard ids.
     */
    readonly release: (address: RunnerAddress, shardId: ShardId) => Effect.Effect<void, PersistenceError>;
    /**
     * Release all the shards assigned to the given runner.
     */
    readonly releaseAll: (address: RunnerAddress) => Effect.Effect<void, PersistenceError>;
}>;
/**
 * Represents a generic interface to the persistent storage required by the
 * cluster.
 *
 * @since 1.0.0
 * @category models
 */
export declare class RunnerStorage extends RunnerStorage_base {
}
/**
 * @since 1.0.0
 * @category Encoded
 */
export interface Encoded {
    /**
     * Get all runners registered with the cluster.
     */
    readonly getRunners: Effect.Effect<Array<readonly [runner: string, healthy: boolean]>, PersistenceError>;
    /**
     * Register a new runner with the cluster.
     */
    readonly register: (address: string, runner: string, healthy: boolean) => Effect.Effect<number, PersistenceError>;
    /**
     * Unregister the runner with the given address.
     */
    readonly unregister: (address: string) => Effect.Effect<void, PersistenceError>;
    /**
     * Set the health status of the given runner.
     */
    readonly setRunnerHealth: (address: string, healthy: boolean) => Effect.Effect<void, PersistenceError>;
    /**
     * Acquire the lock on the given shards, returning the shards that were
     * successfully locked.
     */
    readonly acquire: (address: string, shardIds: NonEmptyArray<string>) => Effect.Effect<Array<string>, PersistenceError>;
    /**
     * Refresh the lock on the given shards, returning the shards that were
     * successfully locked.
     */
    readonly refresh: (address: string, shardIds: Array<string>) => Effect.Effect<ReadonlyArray<string>, PersistenceError>;
    /**
     * Release the lock on the given shard.
     */
    readonly release: (address: string, shardId: string) => Effect.Effect<void, PersistenceError>;
    /**
     * Release the lock on all shards for the given runner.
     */
    readonly releaseAll: (address: string) => Effect.Effect<void, PersistenceError>;
}
/**
 * @since 1.0.0
 * @category layers
 */
export declare const makeEncoded: (encoded: Encoded) => {
    /**
     * Register a new runner with the cluster.
     */
    readonly register: (runner: Runner, healthy: boolean) => Effect.Effect<MachineId.MachineId, PersistenceError>;
    /**
     * Unregister the runner with the given address.
     */
    readonly unregister: (address: RunnerAddress) => Effect.Effect<void, PersistenceError>;
    /**
     * Get all runners registered with the cluster.
     */
    readonly getRunners: Effect.Effect<Array<readonly [runner: Runner, healthy: boolean]>, PersistenceError>;
    /**
     * Set the health status of the given runner.
     */
    readonly setRunnerHealth: (address: RunnerAddress, healthy: boolean) => Effect.Effect<void, PersistenceError>;
    /**
     * Try to acquire the given shard ids for processing.
     *
     * It returns an array of shards it was able to acquire.
     */
    readonly acquire: (address: RunnerAddress, shardIds: Iterable<ShardId>) => Effect.Effect<Array<ShardId>, PersistenceError>;
    /**
     * Refresh the locks owned by the given runner.
     */
    readonly refresh: (address: RunnerAddress, shardIds: Iterable<ShardId>) => Effect.Effect<Array<ShardId>, PersistenceError>;
    /**
     * Release the given shard ids.
     */
    readonly release: (address: RunnerAddress, shardId: ShardId) => Effect.Effect<void, PersistenceError>;
    /**
     * Release all the shards assigned to the given runner.
     */
    readonly releaseAll: (address: RunnerAddress) => Effect.Effect<void, PersistenceError>;
};
/**
 * @since 1.0.0
 * @category constructors
 */
export declare const makeMemory: Effect.Effect<{
    /**
     * Register a new runner with the cluster.
     */
    readonly register: (runner: Runner, healthy: boolean) => Effect.Effect<MachineId.MachineId, PersistenceError>;
    /**
     * Unregister the runner with the given address.
     */
    readonly unregister: (address: RunnerAddress) => Effect.Effect<void, PersistenceError>;
    /**
     * Get all runners registered with the cluster.
     */
    readonly getRunners: Effect.Effect<Array<readonly [runner: Runner, healthy: boolean]>, PersistenceError>;
    /**
     * Set the health status of the given runner.
     */
    readonly setRunnerHealth: (address: RunnerAddress, healthy: boolean) => Effect.Effect<void, PersistenceError>;
    /**
     * Try to acquire the given shard ids for processing.
     *
     * It returns an array of shards it was able to acquire.
     */
    readonly acquire: (address: RunnerAddress, shardIds: Iterable<ShardId>) => Effect.Effect<Array<ShardId>, PersistenceError>;
    /**
     * Refresh the locks owned by the given runner.
     */
    readonly refresh: (address: RunnerAddress, shardIds: Iterable<ShardId>) => Effect.Effect<Array<ShardId>, PersistenceError>;
    /**
     * Release the given shard ids.
     */
    readonly release: (address: RunnerAddress, shardId: ShardId) => Effect.Effect<void, PersistenceError>;
    /**
     * Release all the shards assigned to the given runner.
     */
    readonly releaseAll: (address: RunnerAddress) => Effect.Effect<void, PersistenceError>;
}, never, never>;
/**
 * @since 1.0.0
 * @category layers
 */
export declare const layerMemory: Layer.Layer<RunnerStorage>;
export {};
//# sourceMappingURL=RunnerStorage.d.ts.map