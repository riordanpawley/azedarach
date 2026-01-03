"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.retry = exports.raceAll = exports.make = exports.idempotencyKey = exports.TypeId = exports.CurrentAttempt = void 0;
var Cause = _interopRequireWildcard(require("effect/Cause"));
var Context = _interopRequireWildcard(require("effect/Context"));
var Effect = _interopRequireWildcard(require("effect/Effect"));
var Effectable = _interopRequireWildcard(require("effect/Effectable"));
var _Function = require("effect/Function");
var Schedule = _interopRequireWildcard(require("effect/Schedule"));
var Schema = _interopRequireWildcard(require("effect/Schema"));
var DurableDeferred = _interopRequireWildcard(require("./DurableDeferred.js"));
var _crypto = require("./internal/crypto.js");
var Workflow = _interopRequireWildcard(require("./Workflow.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 * @category Symbols
 */
const TypeId = exports.TypeId = /*#__PURE__*/Symbol.for("@effect/workflow/Activity");
/**
 * @since 1.0.0
 * @category Constructors
 */
const make = options => {
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
exports.make = make;
const interruptRetryPolicy = /*#__PURE__*/Schedule.exponential(100, 1.5).pipe(/*#__PURE__*/Schedule.union(/*#__PURE__*/Schedule.spaced("10 seconds")), /*#__PURE__*/Schedule.union(/*#__PURE__*/Schedule.recurs(10)), /*#__PURE__*/Schedule.whileInput(cause => Cause.isInterrupted(cause)));
const retryOnInterrupt = (name, policy = interruptRetryPolicy) => effect => effect.pipe(Effect.sandbox, Effect.retry(policy), Effect.catchAll(cause => {
  if (!Cause.isInterrupted(cause)) return Effect.failCause(cause);
  return Effect.die(`Activity "${name}" interrupted and retry attempts exhausted`);
}));
/**
 * @since 1.0.0
 * @category Error handling
 */
const retry = exports.retry = /*#__PURE__*/(0, _Function.dual)(2, (effect, options) => Effect.suspend(() => {
  let attempt = 1;
  return Effect.suspend(() => Effect.provideService(effect, CurrentAttempt, attempt++)).pipe(Effect.retry(options));
}));
/**
 * @since 1.0.0
 * @category Attempts
 */
class CurrentAttempt extends /*#__PURE__*/Context.Reference()("@effect/workflow/Activity/CurrentAttempt", {
  defaultValue: () => 1
}) {}
/**
 * @since 1.0.0
 * @category Idempotency
 */
exports.CurrentAttempt = CurrentAttempt;
const idempotencyKey = exports.idempotencyKey = /*#__PURE__*/Effect.fnUntraced(function* (name, options) {
  const instance = yield* InstanceTag;
  let key = `${instance.executionId}`;
  if (options?.includeAttempt) {
    const attempt = yield* CurrentAttempt;
    key += `-${attempt}`;
  }
  key += `-${name}`;
  return yield* (0, _crypto.makeHashDigest)(key);
});
/**
 * @since 1.0.0
 * @category Racing
 */
const raceAll = (name, activities) => DurableDeferred.raceAll({
  name: `Activity/${name}`,
  success: Schema.Union(...activities.map(activity => activity.successSchema)),
  error: Schema.Union(...activities.map(activity => activity.errorSchema)),
  effects: activities
});
// -----------------------------------------------------------------------------
// internal
// -----------------------------------------------------------------------------
exports.raceAll = raceAll;
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