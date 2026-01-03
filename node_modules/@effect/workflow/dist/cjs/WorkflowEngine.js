"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.makeUnsafe = exports.layerMemory = exports.WorkflowInstance = exports.WorkflowEngine = void 0;
var Context = _interopRequireWildcard(require("effect/Context"));
var Effect = _interopRequireWildcard(require("effect/Effect"));
var Exit = _interopRequireWildcard(require("effect/Exit"));
var Fiber = _interopRequireWildcard(require("effect/Fiber"));
var FiberMap = _interopRequireWildcard(require("effect/FiberMap"));
var Layer = _interopRequireWildcard(require("effect/Layer"));
var Option = _interopRequireWildcard(require("effect/Option"));
var Schedule = _interopRequireWildcard(require("effect/Schedule"));
var Schema = _interopRequireWildcard(require("effect/Schema"));
var Scope = _interopRequireWildcard(require("effect/Scope"));
var Workflow = _interopRequireWildcard(require("./Workflow.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 4.0.0
 * @category Services
 */
class WorkflowEngine extends /*#__PURE__*/Context.Tag("@effect/workflow/WorkflowEngine")() {}
/**
 * @since 4.0.0
 * @category Services
 */
exports.WorkflowEngine = WorkflowEngine;
class WorkflowInstance extends /*#__PURE__*/Context.Tag("@effect/workflow/WorkflowEngine/WorkflowInstance")() {
  static initial(workflow, executionId) {
    return WorkflowInstance.of({
      executionId,
      workflow,
      scope: Effect.runSync(Scope.make()),
      suspended: false,
      interrupted: false,
      cause: undefined,
      activityState: {
        count: 0,
        latch: Effect.unsafeMakeLatch()
      }
    });
  }
}
/**
 * @since 4.0.0
 * @category Constructors
 */
exports.WorkflowInstance = WorkflowInstance;
const makeUnsafe = options => WorkflowEngine.of({
  register: Effect.fnUntraced(function* (workflow, execute) {
    const context = yield* Effect.context();
    yield* options.register(workflow, (payload, executionId) => Effect.suspend(() => execute(payload, executionId)).pipe(Effect.mapInputContext(input => Context.merge(context, input))));
  }),
  execute: Effect.fnUntraced(function* (self, opts) {
    const payload = opts.payload;
    const executionId = opts.executionId;
    const suspendedRetrySchedule = opts.suspendedRetrySchedule ?? defaultRetrySchedule;
    yield* Effect.annotateCurrentSpan({
      executionId
    });
    let result;
    // link interruption with parent workflow
    const parentInstance = yield* Effect.serviceOption(WorkflowInstance);
    if (Option.isSome(parentInstance)) {
      const instance = parentInstance.value;
      yield* Effect.addFinalizer(() => {
        if (!instance.interrupted || result?._tag === "Complete") {
          return Effect.void;
        }
        return options.interrupt(self, executionId);
      });
    }
    if (opts.discard) {
      yield* options.execute(self, {
        executionId,
        payload: payload,
        discard: true
      });
      return executionId;
    }
    const run = options.execute(self, {
      executionId,
      payload: payload,
      discard: false,
      parent: Option.getOrUndefined(parentInstance)
    });
    if (Option.isSome(parentInstance)) {
      result = yield* Workflow.wrapActivityResult(run, result => result._tag === "Suspended");
      if (result._tag === "Suspended") {
        return yield* Workflow.suspend(parentInstance.value);
      }
      return yield* result.exit;
    }
    let sleep;
    while (true) {
      result = yield* run;
      if (result._tag === "Complete") {
        return yield* result.exit;
      }
      sleep ??= (yield* Schedule.driver(suspendedRetrySchedule)).next(void 0).pipe(Effect.catchAll(() => Effect.dieMessage(`${self.name}.execute: suspendedRetrySchedule exhausted`)));
      yield* sleep;
    }
  }),
  poll: options.poll,
  interrupt: options.interrupt,
  resume: options.resume,
  activityExecute: Effect.fnUntraced(function* (activity, attempt) {
    const result = yield* options.activityExecute(activity, attempt);
    if (result._tag === "Suspended") {
      return result;
    }
    const exit = yield* Effect.orDie(Schema.decode(activity.exitSchema)(result.exit));
    return new Workflow.Complete({
      exit
    });
  }),
  deferredResult: Effect.fnUntraced(function* (deferred) {
    const instance = yield* WorkflowInstance;
    yield* Effect.annotateCurrentSpan({
      executionId: instance.executionId
    });
    const exit = yield* options.deferredResult(deferred);
    if (exit === undefined) {
      return exit;
    }
    return yield* Effect.orDie(Schema.decodeUnknown(deferred.exitSchema)(exit));
  }),
  deferredDone: Effect.fnUntraced(function* (deferred, opts) {
    return yield* options.deferredDone({
      workflowName: opts.workflowName,
      executionId: opts.executionId,
      deferredName: opts.deferredName,
      exit: yield* Schema.encode(deferred.exitSchema)(opts.exit)
    });
  }),
  scheduleClock: options.scheduleClock
});
exports.makeUnsafe = makeUnsafe;
const defaultRetrySchedule = /*#__PURE__*/Schedule.exponential(200, 1.5).pipe(/*#__PURE__*/Schedule.either(/*#__PURE__*/Schedule.spaced(30000)));
/**
 * @since 1.0.0
 * @category In-memory
 */
const layerMemory = exports.layerMemory = /*#__PURE__*/Layer.scoped(WorkflowEngine, /*#__PURE__*/Effect.gen(function* () {
  const scope = yield* Effect.scope;
  const workflows = new Map();
  const executions = new Map();
  const activities = new Map();
  const resume = Effect.fnUntraced(function* (executionId) {
    const state = executions.get(executionId);
    if (!state) return;
    const exit = state.fiber?.unsafePoll();
    if (exit && exit._tag === "Success" && exit.value._tag === "Complete") {
      return;
    } else if (state.fiber && !exit) {
      return;
    }
    const entry = workflows.get(state.instance.workflow.name);
    const instance = WorkflowInstance.initial(state.instance.workflow, state.instance.executionId);
    instance.interrupted = state.instance.interrupted;
    state.instance = instance;
    state.fiber = yield* state.execute(state.payload, state.instance.executionId).pipe(Effect.onExit(() => {
      if (!instance.interrupted) {
        return Effect.void;
      }
      instance.suspended = false;
      return Effect.withFiberRuntime(fiber => Effect.interruptible(Fiber.interrupt(fiber)));
    }), Workflow.intoResult, Effect.provideService(WorkflowInstance, instance), Effect.provideService(WorkflowEngine, engine), Effect.tap(result => {
      if (!state.parent || result._tag !== "Complete") {
        return Effect.void;
      }
      return Effect.forkIn(resume(state.parent), scope);
    }), Effect.forkIn(entry.scope));
  });
  const deferredResults = new Map();
  const clocks = yield* FiberMap.make();
  const engine = makeUnsafe({
    register: Effect.fnUntraced(function* (workflow, execute) {
      workflows.set(workflow.name, {
        workflow,
        execute,
        scope: yield* Effect.scope
      });
    }),
    execute: Effect.fnUntraced(function* (workflow, options) {
      const entry = workflows.get(workflow.name);
      if (!entry) {
        return yield* Effect.die(`Workflow ${workflow.name} is not registered`);
      }
      let state = executions.get(options.executionId);
      if (!state) {
        state = {
          payload: options.payload,
          execute: entry.execute,
          instance: WorkflowInstance.initial(workflow, options.executionId),
          fiber: undefined,
          parent: options.parent?.executionId
        };
        executions.set(options.executionId, state);
        yield* resume(options.executionId);
      }
      if (options.discard) return;
      return yield* Fiber.join(state.fiber);
    }),
    interrupt: Effect.fnUntraced(function* (_workflow, executionId) {
      const state = executions.get(executionId);
      if (!state) return;
      state.instance.interrupted = true;
      yield* resume(executionId);
    }),
    resume(_workflow, executionId) {
      return resume(executionId);
    },
    activityExecute: Effect.fnUntraced(function* (activity, attempt) {
      const instance = yield* WorkflowInstance;
      const activityId = `${instance.executionId}/${activity.name}/${attempt}`;
      let state = activities.get(activityId);
      if (state) {
        const exit = state.exit;
        if (exit && exit._tag === "Success" && exit.value._tag === "Suspended") {
          state.exit = undefined;
        } else if (exit) {
          return yield* exit;
        }
      } else {
        state = {
          exit: undefined
        };
        activities.set(activityId, state);
      }
      const activityInstance = WorkflowInstance.initial(instance.workflow, instance.executionId);
      activityInstance.interrupted = instance.interrupted;
      return yield* activity.executeEncoded.pipe(Workflow.intoResult, Effect.provideService(WorkflowInstance, activityInstance), Effect.onExit(exit => {
        state.exit = exit;
        return Effect.void;
      }));
    }),
    poll: (_workflow, executionId) => Effect.suspend(() => {
      const state = executions.get(executionId);
      if (!state) {
        return Effect.succeed(undefined);
      }
      const exit = state.fiber?.unsafePoll();
      return exit ?? Effect.succeed(undefined);
    }),
    deferredResult: Effect.fnUntraced(function* (deferred) {
      const instance = yield* WorkflowInstance;
      const id = `${instance.executionId}/${deferred.name}`;
      return deferredResults.get(id);
    }),
    deferredDone: options => Effect.suspend(() => {
      const id = `${options.executionId}/${options.deferredName}`;
      if (deferredResults.has(id)) return Effect.void;
      deferredResults.set(id, options.exit);
      return resume(options.executionId);
    }),
    scheduleClock: (workflow, options) => engine.deferredDone(options.clock.deferred, {
      workflowName: workflow.name,
      executionId: options.executionId,
      deferredName: options.clock.deferred.name,
      exit: Exit.void
    }).pipe(Effect.delay(options.clock.duration), FiberMap.run(clocks, `${options.executionId}/${options.clock.name}`, {
      onlyIfMissing: true
    }), Effect.asVoid)
  });
  return engine;
}));
//# sourceMappingURL=WorkflowEngine.js.map