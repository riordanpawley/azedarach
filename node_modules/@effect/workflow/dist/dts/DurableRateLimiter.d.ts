/**
 * @since 1.0.0
 */
import * as RateLimiter from "@effect/experimental/RateLimiter";
import type * as Duration from "effect/Duration";
import type * as Schema from "effect/Schema";
import * as Activity from "./Activity.js";
/**
 * @since 1.0.0
 * @category Accessors
 */
export declare const rateLimit: (options: {
    readonly name: string;
    readonly algorithm?: "fixed-window" | "token-bucket" | undefined;
    readonly window: Duration.DurationInput;
    readonly limit: number;
    readonly key: string;
    readonly tokens?: number | undefined;
}) => Activity.Activity<typeof Schema.Void, typeof RateLimiter.RateLimitStoreError, RateLimiter.RateLimiter>;
//# sourceMappingURL=DurableRateLimiter.d.ts.map