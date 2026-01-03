/**
 * @since 1.0.0
 */
import * as Config from "effect/Config";
import * as ConfigProvider from "effect/ConfigProvider";
import * as Context from "effect/Context";
import * as Duration from "effect/Duration";
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import * as Option from "effect/Option";
import { RunnerAddress } from "./RunnerAddress.js";
/**
 * Represents the configuration for the `Sharding` service on a given runner.
 *
 * @since 1.0.0
 * @category models
 */
export class ShardingConfig extends /*#__PURE__*/Context.Tag("@effect/cluster/ShardingConfig")() {}
const defaultRunnerAddress = /*#__PURE__*/RunnerAddress.make({
  host: "localhost",
  port: 34431
});
/**
 * @since 1.0.0
 * @category defaults
 */
export const defaults = {
  runnerAddress: /*#__PURE__*/Option.some(defaultRunnerAddress),
  runnerListenAddress: /*#__PURE__*/Option.none(),
  runnerShardWeight: 1,
  shardsPerGroup: 300,
  shardGroups: ["default"],
  preemptiveShutdown: true,
  shardLockRefreshInterval: /*#__PURE__*/Duration.seconds(10),
  shardLockExpiration: /*#__PURE__*/Duration.seconds(35),
  shardLockDisableAdvisory: false,
  entityMailboxCapacity: 4096,
  entityMaxIdleTime: /*#__PURE__*/Duration.minutes(1),
  entityRegistrationTimeout: /*#__PURE__*/Duration.minutes(1),
  entityTerminationTimeout: /*#__PURE__*/Duration.seconds(15),
  entityMessagePollInterval: /*#__PURE__*/Duration.seconds(10),
  entityReplyPollInterval: /*#__PURE__*/Duration.millis(200),
  sendRetryInterval: /*#__PURE__*/Duration.millis(100),
  refreshAssignmentsInterval: /*#__PURE__*/Duration.seconds(3),
  runnerHealthCheckInterval: /*#__PURE__*/Duration.minutes(1),
  simulateRemoteSerialization: true
};
/**
 * @since 1.0.0
 * @category Layers
 */
export const layer = options => Layer.succeed(ShardingConfig, {
  ...defaults,
  ...options
});
/**
 * @since 1.0.0
 * @category defaults
 */
export const layerDefaults = /*#__PURE__*/layer();
/**
 * @since 1.0.0
 * @category Config
 */
export const config = /*#__PURE__*/Config.all({
  runnerAddress: /*#__PURE__*/Config.all({
    host: Config.string("host").pipe(Config.withDefault(defaultRunnerAddress.host), Config.withDescription("The hostname or IP address of the runner.")),
    port: Config.integer("port").pipe(Config.withDefault(defaultRunnerAddress.port), Config.withDescription("The port used for inter-runner communication."))
  }).pipe(/*#__PURE__*/Config.map(options => RunnerAddress.make(options)), Config.option),
  runnerListenAddress: /*#__PURE__*/Config.all({
    host: Config.string("listenHost").pipe(Config.withDescription("The host to listen on.")),
    port: Config.integer("listenPort").pipe(Config.withDefault(defaultRunnerAddress.port), Config.withDescription("The port to listen on."))
  }).pipe(/*#__PURE__*/Config.map(options => RunnerAddress.make(options)), Config.option),
  runnerShardWeight: /*#__PURE__*/Config.integer("runnerShardWeight").pipe(/*#__PURE__*/Config.withDefault(defaults.runnerShardWeight)),
  shardGroups: /*#__PURE__*/Config.array(Config.string("shardGroups")).pipe(/*#__PURE__*/Config.withDefault(["default"]), /*#__PURE__*/Config.withDescription("The shard groups that are assigned to this runner.")),
  shardsPerGroup: /*#__PURE__*/Config.integer("shardsPerGroup").pipe(/*#__PURE__*/Config.withDefault(defaults.shardsPerGroup), /*#__PURE__*/Config.withDescription("The number of shards to allocate per shard group.")),
  preemptiveShutdown: /*#__PURE__*/Config.boolean("preemptiveShutdown").pipe(/*#__PURE__*/Config.withDefault(defaults.preemptiveShutdown), /*#__PURE__*/Config.withDescription("Start shutting down as soon as an Entity has started shutting down.")),
  shardLockRefreshInterval: /*#__PURE__*/Config.duration("shardLockRefreshInterval").pipe(/*#__PURE__*/Config.withDefault(defaults.shardLockRefreshInterval), /*#__PURE__*/Config.withDescription("Shard lock refresh interval.")),
  shardLockExpiration: /*#__PURE__*/Config.duration("shardLockExpiration").pipe(/*#__PURE__*/Config.withDefault(defaults.shardLockExpiration), /*#__PURE__*/Config.withDescription("Shard lock expiration duration.")),
  shardLockDisableAdvisory: /*#__PURE__*/Config.boolean("shardLockDisableAdvisory").pipe(/*#__PURE__*/Config.withDefault(defaults.shardLockDisableAdvisory), /*#__PURE__*/Config.withDescription("Disable the use of advisory locks for shard locking.")),
  entityMailboxCapacity: /*#__PURE__*/Config.integer("entityMailboxCapacity").pipe(/*#__PURE__*/Config.withDefault(defaults.entityMailboxCapacity), /*#__PURE__*/Config.withDescription("The default capacity of the mailbox for entities.")),
  entityMaxIdleTime: /*#__PURE__*/Config.duration("entityMaxIdleTime").pipe(/*#__PURE__*/Config.withDefault(defaults.entityMaxIdleTime), /*#__PURE__*/Config.withDescription("The maximum duration of inactivity (i.e. without receiving a message) after which an entity will be interrupted.")),
  entityRegistrationTimeout: /*#__PURE__*/Config.duration("entityRegistrationTimeout").pipe(/*#__PURE__*/Config.withDefault(defaults.entityRegistrationTimeout), /*#__PURE__*/Config.withDescription("If an entity does not register itself within this time after a message is sent to it, the message will be marked as failed.")),
  entityTerminationTimeout: /*#__PURE__*/Config.duration("entityTerminationTimeout").pipe(/*#__PURE__*/Config.withDefault(defaults.entityTerminationTimeout), /*#__PURE__*/Config.withDescription("The maximum duration of time to wait for an entity to terminate.")),
  entityMessagePollInterval: /*#__PURE__*/Config.duration("entityMessagePollInterval").pipe(/*#__PURE__*/Config.withDefault(defaults.entityMessagePollInterval), /*#__PURE__*/Config.withDescription("The interval at which to poll for unprocessed messages from storage.")),
  entityReplyPollInterval: /*#__PURE__*/Config.duration("entityReplyPollInterval").pipe(/*#__PURE__*/Config.withDefault(defaults.entityReplyPollInterval), /*#__PURE__*/Config.withDescription("The interval at which to poll for client replies from storage.")),
  sendRetryInterval: /*#__PURE__*/Config.duration("sendRetryInterval").pipe(/*#__PURE__*/Config.withDefault(defaults.sendRetryInterval), /*#__PURE__*/Config.withDescription("The interval to retry a send if EntityNotAssignedToRunner is returned.")),
  refreshAssignmentsInterval: /*#__PURE__*/Config.duration("refreshAssignmentsInterval").pipe(/*#__PURE__*/Config.withDefault(defaults.refreshAssignmentsInterval), /*#__PURE__*/Config.withDescription("The interval at which to refresh shard assignments.")),
  runnerHealthCheckInterval: /*#__PURE__*/Config.duration("runnerHealthCheckInterval").pipe(/*#__PURE__*/Config.withDefault(defaults.runnerHealthCheckInterval), /*#__PURE__*/Config.withDescription("The interval at which to check for unhealthy runners and report them.")),
  simulateRemoteSerialization: /*#__PURE__*/Config.boolean("simulateRemoteSerialization").pipe(/*#__PURE__*/Config.withDefault(defaults.simulateRemoteSerialization), /*#__PURE__*/Config.withDescription("Simulate serialization and deserialization to remote runners for local entities."))
});
/**
 * @since 1.0.0
 * @category Config
 */
export const configFromEnv = /*#__PURE__*/config.pipe(/*#__PURE__*/Effect.withConfigProvider(/*#__PURE__*/ConfigProvider.fromEnv().pipe(ConfigProvider.constantCase)));
/**
 * @since 1.0.0
 * @category Layers
 */
export const layerFromEnv = options => Layer.effect(ShardingConfig, options ? Effect.map(configFromEnv, config => ({
  ...config,
  ...options
})) : configFromEnv);
//# sourceMappingURL=ShardingConfig.js.map