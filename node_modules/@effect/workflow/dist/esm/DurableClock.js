/**
 * @since 1.0.0
 */
import * as Context from "effect/Context";
import * as Duration from "effect/Duration";
import * as Effect from "effect/Effect";
import * as Activity from "./Activity.js";
import * as DurableDeferred from "./DurableDeferred.js";
/**
 * @since 1.0.0
 * @category Symbols
 */
export const TypeId = /*#__PURE__*/Symbol.for("@effect/workflow/DurableClock");
/**
 * @since 1.0.0
 * @category Constructors
 */
export const make = options => ({
  [TypeId]: TypeId,
  name: options.name,
  duration: Duration.decode(options.duration),
  deferred: DurableDeferred.make(`DurableClock/${options.name}`)
});
const EngineTag = /*#__PURE__*/Context.GenericTag("@effect/workflow/WorkflowEngine");
const InstanceTag = /*#__PURE__*/Context.GenericTag("@effect/workflow/WorkflowEngine/WorkflowInstance");
/**
 * @since 1.0.0
 * @category Sleeping
 */
export const sleep = /*#__PURE__*/Effect.fnUntraced(function* (options) {
  const duration = Duration.decode(options.duration);
  if (Duration.isZero(duration)) {
    return;
  }
  const inMemoryThreshold = options.inMemoryThreshold ? Duration.decode(options.inMemoryThreshold) : defaultInMemoryThreshold;
  if (Duration.lessThanOrEqualTo(duration, inMemoryThreshold)) {
    return yield* Activity.make({
      name: `DurableClock/${options.name}`,
      execute: Effect.sleep(duration)
    });
  }
  const engine = yield* EngineTag;
  const instance = yield* InstanceTag;
  const clock = make(options);
  yield* engine.scheduleClock(instance.workflow, {
    executionId: instance.executionId,
    clock
  });
  return yield* DurableDeferred.await(clock.deferred);
});
const defaultInMemoryThreshold = /*#__PURE__*/Duration.seconds(60);
//# sourceMappingURL=DurableClock.js.map