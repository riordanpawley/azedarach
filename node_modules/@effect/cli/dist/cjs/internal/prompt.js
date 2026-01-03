"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.succeed = exports.run = exports.map = exports.isPrompt = exports.flatMap = exports.custom = exports.all = exports.PromptTypeId = void 0;
var Terminal = _interopRequireWildcard(require("@effect/platform/Terminal"));
var Doc = _interopRequireWildcard(require("@effect/printer-ansi/AnsiDoc"));
var Effect = _interopRequireWildcard(require("effect/Effect"));
var Effectable = _interopRequireWildcard(require("effect/Effectable"));
var _Function = require("effect/Function");
var Pipeable = _interopRequireWildcard(require("effect/Pipeable"));
var _action = require("./prompt/action.js");
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/** @internal */
const PromptSymbolKey = "@effect/cli/Prompt";
/** @internal */
const PromptTypeId = exports.PromptTypeId = /*#__PURE__*/Symbol.for(PromptSymbolKey);
/** @internal */
const proto = {
  ...Effectable.CommitPrototype,
  [PromptTypeId]: {
    _Output: _ => _
  },
  commit() {
    return run(this);
  },
  pipe() {
    return Pipeable.pipeArguments(this, arguments);
  }
};
/** @internal */
const isPrompt = u => typeof u === "object" && u != null && PromptTypeId in u;
exports.isPrompt = isPrompt;
const allTupled = arg => {
  if (arg.length === 0) {
    return succeed([]);
  }
  if (arg.length === 1) {
    return map(arg[0], x => [x]);
  }
  let result = map(arg[0], x => [x]);
  for (let i = 1; i < arg.length; i++) {
    const curr = arg[i];
    result = flatMap(result, tuple => map(curr, a => [...tuple, a]));
  }
  return result;
};
/** @internal */
const all = function () {
  if (arguments.length === 1) {
    if (isPrompt(arguments[0])) {
      return map(arguments[0], x => [x]);
    } else if (Array.isArray(arguments[0])) {
      return allTupled(arguments[0]);
    } else {
      const entries = Object.entries(arguments[0]);
      let result = map(entries[0][1], value => ({
        [entries[0][0]]: value
      }));
      if (entries.length === 1) {
        return result;
      }
      const rest = entries.slice(1);
      for (const [key, prompt] of rest) {
        result = result.pipe(flatMap(record => prompt.pipe(map(value => ({
          ...record,
          [key]: value
        })))));
      }
      return result;
    }
  }
  return allTupled(arguments[0]);
};
/** @internal */
exports.all = all;
const custom = (initialState, handlers) => {
  const op = Object.create(proto);
  op._tag = "Loop";
  op.initialState = initialState;
  op.render = handlers.render;
  op.process = handlers.process;
  op.clear = handlers.clear;
  return op;
};
/** @internal */
exports.custom = custom;
const map = exports.map = /*#__PURE__*/(0, _Function.dual)(2, (self, f) => flatMap(self, a => succeed(f(a))));
/** @internal */
const flatMap = exports.flatMap = /*#__PURE__*/(0, _Function.dual)(2, (self, f) => {
  const op = Object.create(proto);
  op._tag = "OnSuccess";
  op.prompt = self;
  op.onSuccess = f;
  return op;
});
/** @internal */
const run = exports.run = /*#__PURE__*/Effect.fnUntraced(function* (self) {
  const terminal = yield* Terminal.Terminal;
  const input = yield* terminal.readInput;
  return yield* runWithInput(self, terminal, input);
}, /*#__PURE__*/Effect.mapError(() => new Terminal.QuitException()), Effect.scoped);
const runWithInput = (prompt, terminal, input) => Effect.suspend(() => {
  const op = prompt;
  switch (op._tag) {
    case "Loop":
      {
        return runLoop(op, terminal, input);
      }
    case "OnSuccess":
      {
        return Effect.flatMap(runWithInput(op.prompt, terminal, input), a => runWithInput(op.onSuccess(a), terminal, input));
      }
    case "Succeed":
      {
        return Effect.succeed(op.value);
      }
  }
});
const runLoop = /*#__PURE__*/Effect.fnUntraced(function* (loop, terminal, input) {
  let state = Effect.isEffect(loop.initialState) ? yield* loop.initialState : loop.initialState;
  let action = _action.Action.NextFrame({
    state
  });
  while (true) {
    const msg = yield* loop.render(state, action);
    yield* Effect.orDie(terminal.display(msg));
    const event = yield* input.take;
    action = yield* loop.process(event, state);
    switch (action._tag) {
      case "Beep":
        continue;
      case "NextFrame":
        {
          yield* Effect.orDie(terminal.display(yield* loop.clear(state, action)));
          state = action.state;
          continue;
        }
      case "Submit":
        {
          yield* Effect.orDie(terminal.display(yield* loop.clear(state, action)));
          const msg = yield* loop.render(state, action);
          yield* Effect.orDie(terminal.display(msg));
          return action.value;
        }
    }
  }
}, (effect, _, terminal) => Effect.ensuring(effect, Effect.orDie(terminal.display(Doc.render(Doc.cursorShow, {
  style: "pretty"
})))));
/** @internal */
const succeed = value => {
  const op = Object.create(proto);
  op._tag = "Succeed";
  op.value = value;
  return op;
};
exports.succeed = succeed;
//# sourceMappingURL=prompt.js.map