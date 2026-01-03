import * as Duration from "effect/Duration";
import * as Effect from "effect/Effect";
import type * as Schema from "effect/Schema";
import * as DurableDeferred from "./DurableDeferred.js";
import type { WorkflowEngine, WorkflowInstance } from "./WorkflowEngine.js";
/**
 * @since 1.0.0
 * @category Symbols
 */
export declare const TypeId: unique symbol;
/**
 * @since 1.0.0
 * @category Symbols
 */
export type TypeId = typeof TypeId;
/**
 * @since 1.0.0
 * @category Models
 */
export interface DurableClock {
    readonly [TypeId]: TypeId;
    readonly name: string;
    readonly duration: Duration.Duration;
    readonly deferred: DurableDeferred.DurableDeferred<typeof Schema.Void>;
}
/**
 * @since 1.0.0
 * @category Constructors
 */
export declare const make: (options: {
    readonly name: string;
    readonly duration: Duration.DurationInput;
}) => DurableClock;
/**
 * @since 1.0.0
 * @category Sleeping
 */
export declare const sleep: (options: {
    readonly name: string;
    readonly duration: Duration.DurationInput;
    /**
     * If the duration is less than or equal to this threshold, the clock will
     * be executed in memory.
     *
     * Defaults to 60 seconds.
     */
    readonly inMemoryThreshold?: Duration.DurationInput | undefined;
}) => Effect.Effect<void, never, WorkflowEngine | WorkflowInstance>;
//# sourceMappingURL=DurableClock.d.ts.map