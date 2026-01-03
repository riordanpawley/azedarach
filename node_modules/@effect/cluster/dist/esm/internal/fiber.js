import * as Cause from "effect/Cause";
import * as Effect from "effect/Effect";
/** @internal */
export const joinAllDiscard = fibers => Effect.async(resume => {
  let cause = undefined;
  let i = 0;
  function loop() {
    while (i < fibers.length) {
      const fiber = fibers[i];
      const exit = fiber.unsafePoll();
      if (exit) {
        i++;
        if (exit._tag === "Success") continue;
        cause = cause ? Cause.parallel(cause, exit.cause) : exit.cause;
        continue;
      }
      fiber.addObserver(onExit);
      return;
    }
    resume(cause ? Effect.failCause(cause) : Effect.void);
  }
  function onExit(exit) {
    i++;
    if (exit._tag === "Failure") {
      cause = cause ? Cause.parallel(cause, exit.cause) : exit.cause;
    }
    loop();
  }
  loop();
  return Effect.sync(() => fibers[i].removeObserver(onExit));
});
//# sourceMappingURL=fiber.js.map