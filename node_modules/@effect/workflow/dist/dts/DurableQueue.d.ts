/**
 * @since 1.0.0
 */
import * as PersistedQueue from "@effect/experimental/PersistedQueue";
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import * as Schedule from "effect/Schedule";
import * as Schema from "effect/Schema";
import * as DurableDeferred from "./DurableDeferred.js";
import type * as WorkflowEngine from "./WorkflowEngine.js";
/**
 * @since 1.0.0
 * @category Type IDs
 */
export type TypeId = "~@effect/workflow/DurableQueue";
/**
 * @since 1.0.0
 * @category Type IDs
 */
export declare const TypeId: TypeId;
/**
 * @since 1.0.0
 * @category Models
 */
export interface DurableQueue<Payload extends Schema.Schema.Any, Success extends Schema.Schema.Any = typeof Schema.Void, Error extends Schema.Schema.All = typeof Schema.Never> {
    readonly [TypeId]: TypeId;
    readonly name: string;
    readonly payloadSchema: Payload;
    readonly idempotencyKey: (payload: Payload["Type"]) => string;
    readonly deferred: DurableDeferred.DurableDeferred<Success, Error>;
}
/**
 * A `DurableQueue` wraps a `PersistedQueue`, providing a way to wait for items
 * to finish processing using a `DurableDeferred`.
 *
 * ```ts
 * import { DurableQueue, Workflow } from "@effect/workflow"
 * import { Effect, Schema } from "effect"
 *
 * // Define a DurableQueue that can be used to derive workers and offer items for
 * // processing.
 * const ApiQueue = DurableQueue.make({
 *   name: "ApiQueue",
 *   payload: {
 *     id: Schema.String
 *   },
 *   success: Schema.Void,
 *   error: Schema.Never,
 *   idempotencyKey(payload) {
 *     return payload.id
 *   }
 * })
 *
 * const MyWorkflow = Workflow.make({
 *   name: "MyWorkflow",
 *   payload: {
 *     id: Schema.String
 *   },
 *   idempotencyKey: ({ id }) => id
 * })
 *
 * const MyWorkflowLayer = MyWorkflow.toLayer(
 *   Effect.fn(function*() {
 *     // Add an item to the DurableQueue defined above.
 *     //
 *     // When the worker has finished processing the item, the workflow will
 *     // resume.
 *     //
 *     yield* DurableQueue.process(ApiQueue, { id: "api-call-1" })
 *
 *     yield* Effect.log("Workflow succeeded!")
 *   })
 * )
 *
 * // Define a worker layer that can process items from the DurableQueue.
 * const ApiWorker = DurableQueue.worker(
 *   ApiQueue,
 *   Effect.fn(function*({ id }) {
 *     yield* Effect.log(`Worker processing API call with id: ${id}`)
 *   }),
 *   { concurrency: 5 } // Process up to 5 items concurrently
 * )
 * ```
 *
 * @since 1.0.0
 * @category Constructors
 */
export declare const make: <Payload extends Schema.Schema.Any | Schema.Struct.Fields, Success extends Schema.Schema.Any = typeof Schema.Void, Error extends Schema.Schema.All = typeof Schema.Never>(options: {
    readonly name: string;
    readonly payload: Payload;
    readonly idempotencyKey: (payload: Payload extends Schema.Struct.Fields ? Schema.Struct<Payload>["Type"] : Payload["Type"]) => string;
    readonly success?: Success | undefined;
    readonly error?: Error | undefined;
}) => DurableQueue<Payload extends Schema.Struct.Fields ? Schema.Struct<Payload> : Payload, Success, Error>;
/**
 * @since 1.0.0
 * @category Processing
 */
export declare const process: <Payload extends Schema.Schema.Any, Success extends Schema.Schema.Any, Error extends Schema.Schema.All>(self: DurableQueue<Payload, Success, Error>, payload: Payload["Type"], options?: {
    readonly retrySchedule?: Schedule.Schedule<any, PersistedQueue.PersistedQueueError> | undefined;
}) => Effect.Effect<Success["Type"], Error["Type"], WorkflowEngine.WorkflowEngine | WorkflowEngine.WorkflowInstance | PersistedQueue.PersistedQueueFactory | Success["Context"] | Error["Context"] | Payload["Context"]>;
/**
 * @since 1.0.0
 * @category Worker
 */
export declare const makeWorker: <Payload extends Schema.Schema.Any, Success extends Schema.Schema.Any, Error extends Schema.Schema.All, R>(self: DurableQueue<Payload, Success, Error>, f: (payload: Payload["Type"]) => Effect.Effect<Success["Type"], Error["Type"], R>, options?: {
    readonly concurrency?: number | undefined;
} | undefined) => Effect.Effect<never, never, WorkflowEngine.WorkflowEngine | PersistedQueue.PersistedQueueFactory | R | Payload["Context"] | Success["Context"] | Error["Context"]>;
/**
 * @since 1.0.0
 * @category Worker
 */
export declare const worker: <Payload extends Schema.Schema.Any, Success extends Schema.Schema.Any, Error extends Schema.Schema.All, R>(self: DurableQueue<Payload, Success, Error>, f: (payload: Payload["Type"]) => Effect.Effect<Success["Type"], Error["Type"], R>, options?: {
    readonly concurrency?: number | undefined;
} | undefined) => Layer.Layer<never, never, WorkflowEngine.WorkflowEngine | PersistedQueue.PersistedQueueFactory | R | Payload["Context"] | Success["Context"] | Error["Context"]>;
//# sourceMappingURL=DurableQueue.d.ts.map