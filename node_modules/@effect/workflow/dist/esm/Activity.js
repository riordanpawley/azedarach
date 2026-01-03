import * as Cause from "effect/Cause";
import * as Context from "effect/Context";
import * as Effect from "effect/Effect";
import * as Effectable from "effect/Effectable";
import { dual } from "effect/Function";
import * as Schedule from "effect/Schedule";
import * as Schema from "effect/Schema";
import * as DurableDeferred from "./DurableDeferred.js";
import { makeHashDigest } from "./internal/crypto.js";
import * as Workflow from "./Workflow.js";
/**
 * @since 1.0.0
 * @category Symbols
 */
export const TypeId = /*#__PURE__*/Symbol.for("@effect/workflow/Activity");
/**
 * @since 1.0.0
 * @category Constructors
 */
export const make = options => {
  const successSchema = options.success ?? Schema.Void;
  const errorSchema = options.error ?? Schema.Never;
  // eslint-disable-next-line prefer-const
  let execute;
  const executeWithoutInterrupt = retryOnInterrupt(options.name, options.interruptRetryPolicy)(options.execute);
  const self = {
    ...Effectable.CommitPrototype,
    [TypeId]: TypeId,
    name: options.name,
    successSchema,
    errorSchema,
    exitSchema: Schema.ExitFromSelf({
      success: successSchema,
      failure: errorSchema,
      defect: Schema.Defect
    }),
    execute: executeWithoutInterrupt,
    executeEncoded: Effect.matchEffect(executeWithoutInterrupt, {
      onFailure: error => Effect.flatMap(Effect.orDie(Schema.encode(self.errorSchema)(error)), Effect.fail),
      onSuccess: value => Effect.orDie(Schema.encode(self.successSchema)(value))
    }),
    commit() {
      return execute;
    }
  };
  execute = makeExecute(self);
  return self;
};
const interruptRetryPolicy = /*#__PURE__*/Schedule.exponential(100, 1.5).pipe(/*#__PURE__*/Schedule.union(/*#__PURE__*/Schedule.spaced("10 seconds")), /*#__PURE__*/Schedule.union(/*#__PURE__*/Schedule.recurs(10)), /*#__PURE__*/Schedule.whileInput(cause => Cause.isInterrupted(cause)));
const retryOnInterrupt = (name, policy = interruptRetryPolicy) => effect => effect.pipe(Effect.sandbox, Effect.retry(policy), Effect.catchAll(cause => {
  if (!Cause.isInterrupted(cause)) return Effect.failCause(cause);
  return Effect.die(`Activity "${name}" interrupted and retry attempts exhausted`);
}));
/**
 * @since 1.0.0
 * @category Error handling
 */
export const retry = /*#__PURE__*/dual(2, (effect, options) => Effect.suspend(() => {
  let attempt = 1;
  return Effect.suspend(() => Effect.provideService(effect, CurrentAttempt, attempt++)).pipe(Effect.retry(options));
}));
/**
 * @since 1.0.0
 * @category Attempts
 */
export class CurrentAttempt extends /*#__PURE__*/Context.Reference()("@effect/workflow/Activity/CurrentAttempt", {
  defaultValue: () => 1
}) {}
/**
 * @since 1.0.0
 * @category Idempotency
 */
export const idempotencyKey = /*#__PURE__*/Effect.fnUntraced(function* (name, options) {
  const instance = yield* InstanceTag;
  let key = `${instance.executionId}`;
  if (options?.includeAttempt) {
    const attempt = yield* CurrentAttempt;
    key += `-${attempt}`;
  }
  key += `-${name}`;
  return yield* makeHashDigest(key);
});
/**
 * @since 1.0.0
 * @category Racing
 */
export const raceAll = (name, activities) => DurableDeferred.raceAll({
  name: `Activity/${name}`,
  success: Schema.Union(...activities.map(activity => activity.successSchema)),
  error: Schema.Union(...activities.map(activity => activity.errorSchema)),
  effects: activities
});
// -----------------------------------------------------------------------------
// internal
// -----------------------------------------------------------------------------
const EngineTag = /*#__PURE__*/Context.GenericTag("@effect/workflow/WorkflowEngine");
const InstanceTag = /*#__PURE__*/Context.GenericTag("@effect/workflow/WorkflowEngine/WorkflowInstance");
const makeExecute = /*#__PURE__*/Effect.fnUntraced(function* (activity) {
  const engine = yield* EngineTag;
  const instance = yield* InstanceTag;
  const attempt = yield* CurrentAttempt;
  yield* Effect.annotateCurrentSpan({
    executionId: instance.executionId
  });
  const result = yield* Workflow.wrapActivityResult(engine.activityExecute(activity, attempt), _ => _._tag === "Suspended");
  if (result._tag === "Suspended") {
    return yield* Workflow.suspend(instance);
  }
  return yield* result.exit;
}, (effect, activity) => Effect.withSpan(effect, activity.name, {
  captureStackTrace: false
}));
//# sourceMappingURL=Activity.js.map