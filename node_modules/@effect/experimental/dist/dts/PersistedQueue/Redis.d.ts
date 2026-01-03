import * as Config from "effect/Config";
import * as Duration from "effect/Duration";
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import type { RedisOptions } from "ioredis";
import * as PersistedQueue from "../PersistedQueue.js";
/**
 * @since 1.0.0
 * @category Constructors
 */
export declare const make: (options: import("ioredis").CommonRedisOptions & import("ioredis").SentinelConnectionOptions & Partial<{
    port: number;
    host?: string | undefined | undefined;
    family?: number | undefined | undefined;
} & {
    path: string;
}> & {
    disconnectTimeout?: number;
    tls?: import("tls").ConnectionOptions;
} & {
    readonly prefix?: string | undefined;
    readonly pollInterval?: Duration.DurationInput | undefined;
    readonly lockRefreshInterval?: Duration.DurationInput | undefined;
    readonly lockExpiration?: Duration.DurationInput | undefined;
}) => Effect.Effect<{
    readonly offer: (options: {
        readonly name: string;
        readonly id: string;
        readonly element: unknown;
        readonly isCustomId: boolean;
    }) => Effect.Effect<void, PersistedQueue.PersistedQueueError>;
    readonly take: (options: {
        readonly name: string;
        readonly maxAttempts: number;
    }) => Effect.Effect<{
        readonly id: string;
        readonly attempts: number;
        readonly element: unknown;
    }, PersistedQueue.PersistedQueueError, import("effect/Scope").Scope>;
}, never, import("effect/Scope").Scope>;
/**
 * @since 1.0.0
 * @category Layers
 */
export declare const layerStore: (options: RedisOptions & {
    readonly prefix?: string | undefined;
    readonly pollInterval?: Duration.DurationInput | undefined;
    readonly lockRefreshInterval?: Duration.DurationInput | undefined;
    readonly lockExpiration?: Duration.DurationInput | undefined;
}) => Layer.Layer<PersistedQueue.PersistedQueueStore, never, never>;
/**
 * @since 1.0.0
 * @category Layers
 */
export declare const layerStoreConfig: (options: Config.Config.Wrap<RedisOptions & {
    readonly prefix?: string | undefined;
}>) => Layer.Layer<PersistedQueue.PersistedQueueStore, import("effect/ConfigError").ConfigError, never>;
//# sourceMappingURL=Redis.d.ts.map