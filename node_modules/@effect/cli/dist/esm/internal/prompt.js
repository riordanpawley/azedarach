import * as Terminal from "@effect/platform/Terminal";
import * as Doc from "@effect/printer-ansi/AnsiDoc";
import * as Effect from "effect/Effect";
import * as Effectable from "effect/Effectable";
import { dual } from "effect/Function";
import * as Pipeable from "effect/Pipeable";
import { Action } from "./prompt/action.js";
/** @internal */
const PromptSymbolKey = "@effect/cli/Prompt";
/** @internal */
export const PromptTypeId = /*#__PURE__*/Symbol.for(PromptSymbolKey);
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
export const isPrompt = u => typeof u === "object" && u != null && PromptTypeId in u;
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
export const all = function () {
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
export const custom = (initialState, handlers) => {
  const op = Object.create(proto);
  op._tag = "Loop";
  op.initialState = initialState;
  op.render = handlers.render;
  op.process = handlers.process;
  op.clear = handlers.clear;
  return op;
};
/** @internal */
export const map = /*#__PURE__*/dual(2, (self, f) => flatMap(self, a => succeed(f(a))));
/** @internal */
export const flatMap = /*#__PURE__*/dual(2, (self, f) => {
  const op = Object.create(proto);
  op._tag = "OnSuccess";
  op.prompt = self;
  op.onSuccess = f;
  return op;
});
/** @internal */
export const run = /*#__PURE__*/Effect.fnUntraced(function* (self) {
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
  let action = Action.NextFrame({
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
export const succeed = value => {
  const op = Object.create(proto);
  op._tag = "Succeed";
  op.value = value;
  return op;
};
//# sourceMappingURL=prompt.js.map