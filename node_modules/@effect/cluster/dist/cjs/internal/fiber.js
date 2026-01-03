"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.joinAllDiscard = void 0;
var Cause = _interopRequireWildcard(require("effect/Cause"));
var Effect = _interopRequireWildcard(require("effect/Effect"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/** @internal */
const joinAllDiscard = fibers => Effect.async(resume => {
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
exports.joinAllDiscard = joinAllDiscard;
//# sourceMappingURL=fiber.js.map