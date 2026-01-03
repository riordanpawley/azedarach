/**
 * @since 1.0.0
 */
import * as Config from "effect/Config";
import type { ConfigError } from "effect/ConfigError";
import * as Context from "effect/Context";
import type { DurationInput } from "effect/Duration";
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import * as Option from "effect/Option";
import { RunnerAddress } from "./RunnerAddress.js";
declare const ShardingConfig_base: Context.TagClass<ShardingConfig, "@effect/cluster/ShardingConfig", {
    /**
     * The address for the current runner that other runners can use to
     * communicate with it.
     *
     * If `None`, the runner is not part of the cluster and will be in a client-only
     * mode.
     */
    readonly runnerAddress: Option.Option<RunnerAddress>;
    /**
     * The listen address for the current runner.
     *
     * Defaults to the `runnerAddress`.
     */
    readonly runnerListenAddress: Option.Option<RunnerAddress>;
    /**
     * A number that determines how many shards this runner will be assigned
     * relative to other runners.
     *
     * Defaults to `1`.
     *
     * A value of `2` means that this runner should be assigned twice as many
     * shards as a runner with a weight of `1`.
     */
    readonly runnerShardWeight: number;
    /**
     * The shard groups that are assigned to this runner.
     *
     * Defaults to `["default"]`.
     */
    readonly shardGroups: ReadonlyArray<string>;
    /**
     * The number of shards to allocate per shard group.
     *
     * **Note**: this value should be consistent across all runners.
     */
    readonly shardsPerGroup: number;
    /**
     * Shard lock refresh interval.
     */
    readonly shardLockRefreshInterval: DurationInput;
    /**
     * Shard lock expiration duration.
     */
    readonly shardLockExpiration: DurationInput;
    /**
     * Disable the use of advisory locks for shard locking.
     */
    readonly shardLockDisableAdvisory: boolean;
    /**
     * Start shutting down as soon as an Entity has started shutting down.
     *
     * Defaults to `true`.
     */
    readonly preemptiveShutdown: boolean;
    /**
     * The default capacity of the mailbox for entities.
     */
    readonly entityMailboxCapacity: number | "unbounded";
    /**
     * The maximum duration of inactivity (i.e. without receiving a message)
     * after which an entity will be interrupted.
     */
    readonly entityMaxIdleTime: DurationInput;
    /**
     * If an entity does not register itself within this time after a message is
     * sent to it, the message will be marked as failed.
     *
     * Defaults to 1 minute.
     */
    readonly entityRegistrationTimeout: DurationInput;
    /**
     * The maximum duration of time to wait for an entity to terminate.
     *
     * By default this is set to 15 seconds to stay within kubernetes defaults.
     */
    readonly entityTerminationTimeout: DurationInput;
    /**
     * The interval at which to poll for unprocessed messages from storage.
     */
    readonly entityMessagePollInterval: DurationInput;
    /**
     * The interval at which to poll for client replies from storage.
     */
    readonly entityReplyPollInterval: DurationInput;
    /**
     * The interval at which to poll for new runners and refresh shard
     * assignments.
     */
    readonly refreshAssignmentsInterval: DurationInput;
    /**
     * The interval to retry a send if EntityNotAssignedToRunner is returned.
     */
    readonly sendRetryInterval: DurationInput;
    /**
     * The interval at which to check for unhealthy runners and report them
     */
    readonly runnerHealthCheckInterval: DurationInput;
    /**
     * Simulate serialization and deserialization to remote runners for local
     * entities.
     */
    readonly simulateRemoteSerialization: boolean;
}>;
/**
 * Represents the configuration for the `Sharding` service on a given runner.
 *
 * @since 1.0.0
 * @category models
 */
export declare class ShardingConfig extends ShardingConfig_base {
}
/**
 * @since 1.0.0
 * @category defaults
 */
export declare const defaults: ShardingConfig["Type"];
/**
 * @since 1.0.0
 * @category Layers
 */
export declare const layer: (options?: Partial<ShardingConfig["Type"]>) => Layer.Layer<ShardingConfig>;
/**
 * @since 1.0.0
 * @category defaults
 */
export declare const layerDefaults: Layer.Layer<ShardingConfig>;
/**
 * @since 1.0.0
 * @category Config
 */
export declare const config: Config.Config<ShardingConfig["Type"]>;
/**
 * @since 1.0.0
 * @category Config
 */
export declare const configFromEnv: Effect.Effect<{
    /**
     * The address for the current runner that other runners can use to
     * communicate with it.
     *
     * If `None`, the runner is not part of the cluster and will be in a client-only
     * mode.
     */
    readonly runnerAddress: Option.Option<RunnerAddress>;
    /**
     * The listen address for the current runner.
     *
     * Defaults to the `runnerAddress`.
     */
    readonly runnerListenAddress: Option.Option<RunnerAddress>;
    /**
     * A number that determines how many shards this runner will be assigned
     * relative to other runners.
     *
     * Defaults to `1`.
     *
     * A value of `2` means that this runner should be assigned twice as many
     * shards as a runner with a weight of `1`.
     */
    readonly runnerShardWeight: number;
    /**
     * The shard groups that are assigned to this runner.
     *
     * Defaults to `["default"]`.
     */
    readonly shardGroups: ReadonlyArray<string>;
    /**
     * The number of shards to allocate per shard group.
     *
     * **Note**: this value should be consistent across all runners.
     */
    readonly shardsPerGroup: number;
    /**
     * Shard lock refresh interval.
     */
    readonly shardLockRefreshInterval: DurationInput;
    /**
     * Shard lock expiration duration.
     */
    readonly shardLockExpiration: DurationInput;
    /**
     * Disable the use of advisory locks for shard locking.
     */
    readonly shardLockDisableAdvisory: boolean;
    /**
     * Start shutting down as soon as an Entity has started shutting down.
     *
     * Defaults to `true`.
     */
    readonly preemptiveShutdown: boolean;
    /**
     * The default capacity of the mailbox for entities.
     */
    readonly entityMailboxCapacity: number | "unbounded";
    /**
     * The maximum duration of inactivity (i.e. without receiving a message)
     * after which an entity will be interrupted.
     */
    readonly entityMaxIdleTime: DurationInput;
    /**
     * If an entity does not register itself within this time after a message is
     * sent to it, the message will be marked as failed.
     *
     * Defaults to 1 minute.
     */
    readonly entityRegistrationTimeout: DurationInput;
    /**
     * The maximum duration of time to wait for an entity to terminate.
     *
     * By default this is set to 15 seconds to stay within kubernetes defaults.
     */
    readonly entityTerminationTimeout: DurationInput;
    /**
     * The interval at which to poll for unprocessed messages from storage.
     */
    readonly entityMessagePollInterval: DurationInput;
    /**
     * The interval at which to poll for client replies from storage.
     */
    readonly entityReplyPollInterval: DurationInput;
    /**
     * The interval at which to poll for new runners and refresh shard
     * assignments.
     */
    readonly refreshAssignmentsInterval: DurationInput;
    /**
     * The interval to retry a send if EntityNotAssignedToRunner is returned.
     */
    readonly sendRetryInterval: DurationInput;
    /**
     * The interval at which to check for unhealthy runners and report them
     */
    readonly runnerHealthCheckInterval: DurationInput;
    /**
     * Simulate serialization and deserialization to remote runners for local
     * entities.
     */
    readonly simulateRemoteSerialization: boolean;
}, ConfigError, never>;
/**
 * @since 1.0.0
 * @category Layers
 */
export declare const layerFromEnv: (options?: Partial<ShardingConfig["Type"]> | undefined) => Layer.Layer<ShardingConfig, ConfigError>;
export {};
//# sourceMappingURL=ShardingConfig.d.ts.map