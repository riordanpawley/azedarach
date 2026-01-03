/**
 * @since 1.0.0
 */
import * as Context from "effect/Context";
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import type * as ParseResult from "effect/ParseResult";
import * as Schema from "effect/Schema";
import * as Scope from "effect/Scope";
/**
 * @since 1.0.0
 * @category Type IDs
 */
export declare const TypeId: TypeId;
/**
 * @since 1.0.0
 * @category Type IDs
 */
export type TypeId = "~@effect/experimental/PersistedQueue";
/**
 * @since 1.0.0
 * @category Models
 */
export interface PersistedQueue<in out A, out R = never> {
    readonly [TypeId]: TypeId;
    /**
     * Adds an element to the queue. Returns the id of the enqueued element.
     *
     * If an element with the same id already exists in the queue, it will not be
     * added again.
     */
    readonly offer: (value: A, options?: {
        readonly id: string | undefined;
    }) => Effect.Effect<string, PersistedQueueError | ParseResult.ParseError, R>;
    /**
     * Takes an element from the queue.
     * If the queue is empty, it will wait until an element is available.
     *
     * If the returned effect succeeds, the element is marked as processed,
     * otherwise it will be retried according to the provided options.
     *
     * By default, max attempts is set to 10.
     */
    readonly take: <XA, XE, XR>(f: (value: A, metadata: {
        readonly id: string;
        readonly attempts: number;
    }) => Effect.Effect<XA, XE, XR>, options?: {
        readonly maxAttempts?: number | undefined;
    }) => Effect.Effect<XA, XE | PersistedQueueError | ParseResult.ParseError, R | XR>;
}
declare const PersistedQueueFactory_base: Context.TagClass<PersistedQueueFactory, "@effect/experimental/PersistedQueue/PersistedQueueFactory", {
    readonly make: <A, I, R>(options: {
        readonly name: string;
        readonly schema: Schema.Schema<A, I, R>;
    }) => Effect.Effect<PersistedQueue<A, R>>;
}>;
/**
 * @since 1.0.0
 * @category Factory
 */
export declare class PersistedQueueFactory extends PersistedQueueFactory_base {
}
/**
 * @since 1.0.0
 * @category Accessors
 */
export declare const make: <A, I, R>(options: {
    readonly name: string;
    readonly schema: Schema.Schema<A, I, R>;
}) => Effect.Effect<PersistedQueue<A, R>, never, PersistedQueueFactory>;
/**
 * @since 1.0.0
 * @category Factory
 */
export declare const makeFactory: Effect.Effect<{
    readonly make: <A, I, R>(options: {
        readonly name: string;
        readonly schema: Schema.Schema<A, I, R>;
    }) => Effect.Effect<PersistedQueue<A, R>>;
}, never, PersistedQueueStore>;
/**
 * @since 1.0.0
 * @category Factory
 */
export declare const layer: Layer.Layer<PersistedQueueFactory, never, PersistedQueueStore>;
/**
 * @since 1.0.0
 * @category Errors
 */
export declare const ErrorTypeId: ErrorTypeId;
/**
 * @since 1.0.0
 * @category Errors
 */
export type ErrorTypeId = "~@effect/experimental/PersistedQueue/PersistedQueueError";
declare const PersistedQueueError_base: Schema.TaggedErrorClass<PersistedQueueError, "PersistedQueueError", {
    readonly _tag: Schema.tag<"PersistedQueueError">;
} & {
    message: typeof Schema.String;
    cause: Schema.optional<typeof Schema.Defect>;
}>;
/**
 * @since 1.0.0
 * @category Errors
 */
export declare class PersistedQueueError extends PersistedQueueError_base {
    /**
     * @since 1.0.0
     */
    readonly [ErrorTypeId]: ErrorTypeId;
}
declare const PersistedQueueStore_base: Context.TagClass<PersistedQueueStore, "@effect/experimental/PersistedQueue/PersistedQueueStore", {
    readonly offer: (options: {
        readonly name: string;
        readonly id: string;
        readonly element: unknown;
        readonly isCustomId: boolean;
    }) => Effect.Effect<void, PersistedQueueError>;
    readonly take: (options: {
        readonly name: string;
        readonly maxAttempts: number;
    }) => Effect.Effect<{
        readonly id: string;
        readonly attempts: number;
        readonly element: unknown;
    }, PersistedQueueError, Scope.Scope>;
}>;
/**
 * @since 1.0.0
 * @category Store
 */
export declare class PersistedQueueStore extends PersistedQueueStore_base {
}
/**
 * @since 1.0.0
 * @category Store
 */
export declare const layerStoreMemory: Layer.Layer<PersistedQueueStore>;
export {};
//# sourceMappingURL=PersistedQueue.d.ts.map