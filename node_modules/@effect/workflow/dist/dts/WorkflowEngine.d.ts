/**
 * @since 1.0.0
 */
import type * as Cause from "effect/Cause";
import * as Context from "effect/Context";
import * as Effect from "effect/Effect";
import * as Exit from "effect/Exit";
import * as Layer from "effect/Layer";
import * as Schedule from "effect/Schedule";
import * as Schema from "effect/Schema";
import * as Scope from "effect/Scope";
import type * as Activity from "./Activity.js";
import type { DurableClock } from "./DurableClock.js";
import type * as DurableDeferred from "./DurableDeferred.js";
import * as Workflow from "./Workflow.js";
declare const WorkflowEngine_base: Context.TagClass<WorkflowEngine, "@effect/workflow/WorkflowEngine", {
    /**
     * Register a workflow with the engine.
     */
    readonly register: <Name extends string, Payload extends Workflow.AnyStructSchema, Success extends Schema.Schema.Any, Error extends Schema.Schema.All, R>(workflow: Workflow.Workflow<Name, Payload, Success, Error>, execute: (payload: Payload["Type"], executionId: string) => Effect.Effect<Success["Type"], Error["Type"], R>) => Effect.Effect<void, never, Scope.Scope | Exclude<R, WorkflowEngine | WorkflowInstance | Workflow.Execution<Name> | Scope.Scope> | Payload["Context"] | Success["Context"] | Error["Context"]>;
    /**
     * Execute a registered workflow.
     */
    readonly execute: <Name extends string, Payload extends Workflow.AnyStructSchema, Success extends Schema.Schema.Any, Error extends Schema.Schema.All, const Discard extends boolean = false>(workflow: Workflow.Workflow<Name, Payload, Success, Error>, options: {
        readonly executionId: string;
        readonly payload: Payload["Type"];
        readonly discard?: Discard | undefined;
        readonly suspendedRetrySchedule?: Schedule.Schedule<any, unknown> | undefined;
    }) => Effect.Effect<Discard extends true ? string : Success["Type"], Error["Type"], Payload["Context"] | Success["Context"] | Error["Context"]>;
    /**
     * Execute a registered workflow.
     */
    readonly poll: <Name extends string, Payload extends Workflow.AnyStructSchema, Success extends Schema.Schema.Any, Error extends Schema.Schema.All>(workflow: Workflow.Workflow<Name, Payload, Success, Error>, executionId: string) => Effect.Effect<Workflow.Result<Success["Type"], Error["Type"]> | undefined, never, Success["Context"] | Error["Context"]>;
    /**
     * Interrupt a registered workflow.
     */
    readonly interrupt: (workflow: Workflow.Any, executionId: string) => Effect.Effect<void>;
    /**
     * Resume a registered workflow.
     */
    readonly resume: (workflow: Workflow.Any, executionId: string) => Effect.Effect<void>;
    /**
     * Execute an activity from a workflow.
     */
    readonly activityExecute: <Success extends Schema.Schema.Any, Error extends Schema.Schema.All, R>(activity: Activity.Activity<Success, Error, R>, attempt: number) => Effect.Effect<Workflow.Result<Success["Type"], Error["Type"]>, never, Success["Context"] | Error["Context"] | R | WorkflowInstance>;
    /**
     * Try to retrieve the result of an DurableDeferred
     */
    readonly deferredResult: <Success extends Schema.Schema.Any, Error extends Schema.Schema.All>(deferred: DurableDeferred.DurableDeferred<Success, Error>) => Effect.Effect<Exit.Exit<Success["Type"], Error["Type"]> | undefined, never, WorkflowInstance>;
    /**
     * Set the result of a DurableDeferred, and then resume any waiting
     * workflows.
     */
    readonly deferredDone: <Success extends Schema.Schema.Any, Error extends Schema.Schema.All>(deferred: DurableDeferred.DurableDeferred<Success, Error>, options: {
        readonly workflowName: string;
        readonly executionId: string;
        readonly deferredName: string;
        readonly exit: Exit.Exit<Success["Type"], Error["Type"]>;
    }) => Effect.Effect<void, never, Success["Context"] | Error["Context"]>;
    /**
     * Schedule a wake up for a DurableClock
     */
    readonly scheduleClock: (workflow: Workflow.Any, options: {
        readonly executionId: string;
        readonly clock: DurableClock;
    }) => Effect.Effect<void>;
}>;
/**
 * @since 4.0.0
 * @category Services
 */
export declare class WorkflowEngine extends WorkflowEngine_base {
}
declare const WorkflowInstance_base: Context.TagClass<WorkflowInstance, "@effect/workflow/WorkflowEngine/WorkflowInstance", {
    /**
     * The workflow execution ID.
     */
    readonly executionId: string;
    /**
     * The workflow definition.
     */
    readonly workflow: Workflow.Any;
    /**
     * The workflow scope, that represents the lifetime of the workflow.
     */
    readonly scope: Scope.CloseableScope;
    /**
     * Whether the workflow has requested to be suspended.
     */
    suspended: boolean;
    /**
     * Whether the workflow has requested to be interrupted.
     */
    interrupted: boolean;
    /**
     * When SuspendOnFailure is triggered, the cause of the failure is stored
     * here.
     */
    cause: Cause.Cause<never> | undefined;
    readonly activityState: {
        count: number;
        readonly latch: Effect.Latch;
    };
}>;
/**
 * @since 4.0.0
 * @category Services
 */
export declare class WorkflowInstance extends WorkflowInstance_base {
    static initial(workflow: Workflow.Any, executionId: string): WorkflowInstance["Type"];
}
/**
 * @since 4.0.0
 * @category Encoded
 */
export interface Encoded {
    readonly register: (workflow: Workflow.Any, execute: (payload: object, executionId: string) => Effect.Effect<unknown, unknown, WorkflowInstance | WorkflowEngine>) => Effect.Effect<void, never, Scope.Scope>;
    readonly execute: <const Discard extends boolean>(workflow: Workflow.Any, options: {
        readonly executionId: string;
        readonly payload: object;
        readonly discard: Discard;
        readonly parent?: WorkflowInstance["Type"] | undefined;
    }) => Effect.Effect<Discard extends true ? void : Workflow.Result<unknown, unknown>>;
    readonly poll: (workflow: Workflow.Any, executionId: string) => Effect.Effect<Workflow.Result<unknown, unknown> | undefined>;
    readonly interrupt: (workflow: Workflow.Any, executionId: string) => Effect.Effect<void>;
    readonly resume: (workflow: Workflow.Any, executionId: string) => Effect.Effect<void>;
    readonly activityExecute: (activity: Activity.Any, attempt: number) => Effect.Effect<Workflow.Result<unknown, unknown>, never, WorkflowInstance>;
    readonly deferredResult: (deferred: DurableDeferred.Any) => Effect.Effect<Exit.Exit<unknown, unknown> | undefined, never, WorkflowInstance>;
    readonly deferredDone: (options: {
        readonly workflowName: string;
        readonly executionId: string;
        readonly deferredName: string;
        readonly exit: Exit.Exit<unknown, unknown>;
    }) => Effect.Effect<void>;
    readonly scheduleClock: (workflow: Workflow.Any, options: {
        readonly executionId: string;
        readonly clock: DurableClock;
    }) => Effect.Effect<void>;
}
/**
 * @since 4.0.0
 * @category Constructors
 */
export declare const makeUnsafe: (options: Encoded) => WorkflowEngine["Type"];
/**
 * @since 1.0.0
 * @category In-memory
 */
export declare const layerMemory: Layer.Layer<WorkflowEngine>;
export {};
//# sourceMappingURL=WorkflowEngine.d.ts.map