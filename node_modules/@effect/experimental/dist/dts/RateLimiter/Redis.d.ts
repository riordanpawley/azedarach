/**
 * @since 1.0.0
 */
import * as Config from "effect/Config";
import * as Duration from "effect/Duration";
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import type { RedisOptions } from "ioredis";
import * as RateLimiter from "../RateLimiter.js";
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
}) => Effect.Effect<{
    readonly fixedWindow: (options: {
        readonly key: string;
        readonly tokens: number;
        readonly refillRate: Duration.Duration;
        readonly limit: number | undefined;
    }) => Effect.Effect<readonly [count: number, ttl: number], RateLimiter.RateLimiterError>;
    readonly tokenBucket: (options: {
        readonly key: string;
        readonly tokens: number;
        readonly limit: number;
        readonly refillRate: Duration.Duration;
        readonly allowOverflow: boolean;
    }) => Effect.Effect<number, RateLimiter.RateLimiterError>;
}, never, import("effect/Scope").Scope>;
/**
 * @since 1.0.0
 * @category Layers
 */
export declare const layerStore: (options: RedisOptions & {
    readonly prefix?: string | undefined;
}) => Layer.Layer<RateLimiter.RateLimiterStore, never, never>;
/**
 * @since 1.0.0
 * @category Layers
 */
export declare const layerStoreConfig: (options: Config.Config.Wrap<RedisOptions & {
    readonly prefix?: string | undefined;
}>) => Layer.Layer<RateLimiter.RateLimiterStore, import("effect/ConfigError").ConfigError, never>;
//# sourceMappingURL=Redis.d.ts.map