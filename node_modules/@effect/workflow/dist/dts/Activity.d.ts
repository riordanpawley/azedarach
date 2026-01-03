/**
 * @since 1.0.0
 */
import type { NonEmptyReadonlyArray } from "effect/Array";
import * as Cause from "effect/Cause";
import * as Context from "effect/Context";
import * as Effect from "effect/Effect";
import type * as Exit from "effect/Exit";
import * as Schedule from "effect/Schedule";
import * as Schema from "effect/Schema";
import type { Scope } from "effect/Scope";
import type * as Types from "effect/Types";
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
export interface Activity<Success extends Schema.Schema.Any = typeof Schema.Void, Error extends Schema.Schema.All = typeof Schema.Never, R = never> extends Effect.Effect<Success["Type"], Error["Type"], Success["Context"] | Error["Context"] | R | WorkflowEngine | WorkflowInstance> {
    readonly [TypeId]: TypeId;
    readonly name: string;
    readonly successSchema: Success;
    readonly errorSchema: Error;
    readonly exitSchema: Schema.Schema<Exit.Exit<Success["Type"], Error["Type"]>, Exit.Exit<Success["Encoded"], Error["Encoded"]>, Success["Context"] | Error["Context"]>;
    readonly execute: Effect.Effect<Success["Type"], Error["Type"], Success["Context"] | Error["Context"] | R | Scope | WorkflowEngine | WorkflowInstance>;
    readonly executeEncoded: Effect.Effect<Success["Encoded"], Error["Encoded"], Success["Context"] | Error["Context"] | R | Scope | WorkflowEngine | WorkflowInstance>;
}
/**
 * @since 1.0.0
 * @category Models
 */
export interface Any {
    readonly [TypeId]: TypeId;
    readonly name: string;
    readonly successSchema: Schema.Schema.Any;
    readonly errorSchema: Schema.Schema.All;
    readonly execute: Effect.Effect<any, any, any>;
    readonly executeEncoded: Effect.Effect<any, any, any>;
}
/**
 * @since 1.0.0
 * @category Constructors
 */
export declare const make: <R, Success extends Schema.Schema.Any = typeof Schema.Void, Error extends Schema.Schema.All = typeof Schema.Never>(options: {
    readonly name: string;
    readonly success?: Success | undefined;
    readonly error?: Error | undefined;
    readonly execute: Effect.Effect<Success["Type"], Error["Type"], R>;
    readonly interruptRetryPolicy?: Schedule.Schedule<any, Cause.Cause<unknown>> | undefined;
}) => Activity<Success, Error, Exclude<R, WorkflowInstance | WorkflowEngine | Scope>>;
/**
 * @since 1.0.0
 * @category Error handling
 */
export declare const retry: {
    /**
     * @since 1.0.0
     * @category Error handling
     */
    <E, O extends Types.NoExcessProperties<Omit<Effect.Retry.Options<E>, "schedule">, O>>(options: O): <A, R>(self: Effect.Effect<A, E, R>) => Effect.Retry.Return<R, E, A, O>;
    /**
     * @since 1.0.0
     * @category Error handling
     */
    <A, E, R, O extends Types.NoExcessProperties<Omit<Effect.Retry.Options<E>, "schedule">, O>>(self: Effect.Effect<A, E, R>, options: O): Effect.Retry.Return<R, E, A, O>;
};
declare const CurrentAttempt_base: Context.ReferenceClass<CurrentAttempt, "@effect/workflow/Activity/CurrentAttempt", number>;
/**
 * @since 1.0.0
 * @category Attempts
 */
export declare class CurrentAttempt extends CurrentAttempt_base {
}
/**
 * @since 1.0.0
 * @category Idempotency
 */
export declare const idempotencyKey: (name: string, options?: {
    readonly includeAttempt?: boolean | undefined;
} | undefined) => Effect.Effect<string, never, WorkflowInstance>;
/**
 * @since 1.0.0
 * @category Racing
 */
export declare const raceAll: <const Activities extends NonEmptyReadonlyArray<Any>>(name: string, activities: Activities) => Effect.Effect<(Activities[number] extends Activity<infer _A, infer _E, infer _R> ? _A["Type"] : never), (Activities[number] extends Activity<infer _A, infer _E_1, infer _R_1> ? _E_1["Type"] : never), (Activities[number] extends Activity<infer Success, infer Error, infer R> ? Success["Context"] | Error["Context"] | R : never) | WorkflowEngine | WorkflowInstance>;
export {};
//# sourceMappingURL=Activity.d.ts.map