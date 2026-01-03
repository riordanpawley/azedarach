import { DurableDeferred } from "@effect/workflow";
import * as Activity from "@effect/workflow/Activity";
import * as DurableClock from "@effect/workflow/DurableClock";
import * as Workflow from "@effect/workflow/Workflow";
import { WorkflowEngine, WorkflowInstance } from "@effect/workflow/WorkflowEngine";
import * as Effect from "effect/Effect";
import * as Exit from "effect/Exit";
import * as Layer from "effect/Layer";
import * as Schedule from "effect/Schedule";
import * as Schema from "effect/Schema";
import type * as Scope from "effect/Scope";
import { MessageStorage } from "./MessageStorage.js";
import * as Sharding from "./Sharding.js";
/**
 * @since 1.0.0
 * @category Constructors
 */
export declare const make: Effect.Effect<{
    readonly register: <Name extends string, Payload extends Workflow.AnyStructSchema, Success extends Schema.Schema.Any, Error extends Schema.Schema.All, R>(workflow: Workflow.Workflow<Name, Payload, Success, Error>, execute: (payload: Payload["Type"], executionId: string) => Effect.Effect<Success["Type"], Error["Type"], R>) => Effect.Effect<void, never, Scope.Scope | Exclude<R, WorkflowEngine | WorkflowInstance | Workflow.Execution<Name> | Scope.Scope> | Payload["Context"] | Success["Context"] | Error["Context"]>;
    readonly execute: <Name extends string, Payload extends Workflow.AnyStructSchema, Success extends Schema.Schema.Any, Error extends Schema.Schema.All, const Discard extends boolean = false>(workflow: Workflow.Workflow<Name, Payload, Success, Error>, options: {
        readonly executionId: string;
        readonly payload: Payload["Type"];
        readonly discard?: Discard | undefined;
        readonly suspendedRetrySchedule?: Schedule.Schedule<any, unknown> | undefined;
    }) => Effect.Effect<Discard extends true ? string : Success["Type"], Error["Type"], Payload["Context"] | Success["Context"] | Error["Context"]>;
    readonly poll: <Name extends string, Payload extends Workflow.AnyStructSchema, Success extends Schema.Schema.Any, Error extends Schema.Schema.All>(workflow: Workflow.Workflow<Name, Payload, Success, Error>, executionId: string) => Effect.Effect<Workflow.Result<Success["Type"], Error["Type"]> | undefined, never, Success["Context"] | Error["Context"]>;
    readonly interrupt: (workflow: Workflow.Any, executionId: string) => Effect.Effect<void>;
    readonly resume: (workflow: Workflow.Any, executionId: string) => Effect.Effect<void>;
    readonly activityExecute: <Success extends Schema.Schema.Any, Error extends Schema.Schema.All, R>(activity: Activity.Activity<Success, Error, R>, attempt: number) => Effect.Effect<Workflow.Result<Success["Type"], Error["Type"]>, never, Success["Context"] | Error["Context"] | R | WorkflowInstance>;
    readonly deferredResult: <Success extends Schema.Schema.Any, Error extends Schema.Schema.All>(deferred: DurableDeferred.DurableDeferred<Success, Error>) => Effect.Effect<Exit.Exit<Success["Type"], Error["Type"]> | undefined, never, WorkflowInstance>;
    readonly deferredDone: <Success extends Schema.Schema.Any, Error extends Schema.Schema.All>(deferred: DurableDeferred.DurableDeferred<Success, Error>, options: {
        readonly workflowName: string;
        readonly executionId: string;
        readonly deferredName: string;
        readonly exit: Exit.Exit<Success["Type"], Error["Type"]>;
    }) => Effect.Effect<void, never, Success["Context"] | Error["Context"]>;
    readonly scheduleClock: (workflow: Workflow.Any, options: {
        readonly executionId: string;
        readonly clock: DurableClock.DurableClock;
    }) => Effect.Effect<void>;
}, never, Scope.Scope | MessageStorage | Sharding.Sharding>;
/**
 * @since 1.0.0
 * @category Layers
 */
export declare const layer: Layer.Layer<WorkflowEngine, never, Sharding.Sharding | MessageStorage>;
//# sourceMappingURL=ClusterWorkflowEngine.d.ts.map