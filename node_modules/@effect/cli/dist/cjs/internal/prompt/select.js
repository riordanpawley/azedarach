"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.handleClear = handleClear;
exports.select = void 0;
var Terminal = _interopRequireWildcard(require("@effect/platform/Terminal"));
var Ansi = _interopRequireWildcard(require("@effect/printer-ansi/Ansi"));
var Doc = _interopRequireWildcard(require("@effect/printer-ansi/AnsiDoc"));
var Optimize = _interopRequireWildcard(require("@effect/printer/Optimize"));
var Arr = _interopRequireWildcard(require("effect/Array"));
var Effect = _interopRequireWildcard(require("effect/Effect"));
var InternalPrompt = _interopRequireWildcard(require("../prompt.js"));
var _action = require("./action.js");
var InternalAnsiUtils = _interopRequireWildcard(require("./ansi-utils.js"));
var _utils = require("./utils.js");
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
const renderBeep = /*#__PURE__*/Doc.render(Doc.beep, {
  style: "pretty"
});
const NEWLINE_REGEX = /\r?\n/;
function renderOutput(leadingSymbol, trailingSymbol, options) {
  const annotateLine = line => Doc.annotate(Doc.text(line), Ansi.bold);
  const prefix = Doc.cat(leadingSymbol, Doc.space);
  return Arr.match(options.message.split(NEWLINE_REGEX), {
    onEmpty: () => Doc.hsep([prefix, trailingSymbol]),
    onNonEmpty: promptLines => {
      const lines = Arr.map(promptLines, line => annotateLine(line));
      return prefix.pipe(Doc.cat(Doc.nest(Doc.vsep(lines), 2)), Doc.cat(Doc.space), Doc.cat(trailingSymbol), Doc.cat(Doc.space));
    }
  });
}
function renderChoicePrefix(state, choices, toDisplay, currentIndex, figures) {
  let prefix = Doc.space;
  if (currentIndex === toDisplay.startIndex && toDisplay.startIndex > 0) {
    prefix = figures.arrowUp;
  } else if (currentIndex === toDisplay.endIndex - 1 && toDisplay.endIndex < choices.length) {
    prefix = figures.arrowDown;
  }
  if (choices[currentIndex].disabled) {
    const annotation = Ansi.combine(Ansi.bold, Ansi.blackBright);
    return state === currentIndex ? figures.pointer.pipe(Doc.annotate(annotation), Doc.cat(prefix)) : prefix.pipe(Doc.cat(Doc.space));
  }
  return state === currentIndex ? figures.pointer.pipe(Doc.annotate(Ansi.cyanBright), Doc.cat(prefix)) : prefix.pipe(Doc.cat(Doc.space));
}
function renderChoiceTitle(choice, isSelected) {
  const title = Doc.text(choice.title);
  if (isSelected) {
    return choice.disabled ? Doc.annotate(title, Ansi.combine(Ansi.underlined, Ansi.blackBright)) : Doc.annotate(title, Ansi.combine(Ansi.underlined, Ansi.cyanBright));
  }
  return choice.disabled ? Doc.annotate(title, Ansi.combine(Ansi.strikethrough, Ansi.blackBright)) : title;
}
function renderChoiceDescription(choice, isSelected) {
  if (!choice.disabled && choice.description && isSelected) {
    return Doc.char("-").pipe(Doc.cat(Doc.space), Doc.cat(Doc.text(choice.description)), Doc.annotate(Ansi.blackBright));
  }
  return Doc.empty;
}
function renderChoices(state, options, figures) {
  const choices = options.choices;
  const toDisplay = (0, _utils.entriesToDisplay)(state, choices.length, options.maxPerPage);
  const documents = [];
  for (let index = toDisplay.startIndex; index < toDisplay.endIndex; index++) {
    const choice = choices[index];
    const isSelected = state === index;
    const prefix = renderChoicePrefix(state, choices, toDisplay, index, figures);
    const title = renderChoiceTitle(choice, isSelected);
    const description = renderChoiceDescription(choice, isSelected);
    documents.push(prefix.pipe(Doc.cat(title), Doc.cat(Doc.space), Doc.cat(description)));
  }
  return Doc.vsep(documents);
}
function renderNextFrame(state, options) {
  return Effect.gen(function* () {
    const terminal = yield* Terminal.Terminal;
    const columns = yield* terminal.columns;
    const figures = yield* InternalAnsiUtils.figures;
    const choices = renderChoices(state, options, figures);
    const leadingSymbol = Doc.annotate(Doc.text("?"), Ansi.cyanBright);
    const trailingSymbol = Doc.annotate(figures.pointerSmall, Ansi.blackBright);
    const promptMsg = renderOutput(leadingSymbol, trailingSymbol, options);
    return Doc.cursorHide.pipe(Doc.cat(promptMsg), Doc.cat(Doc.hardLine), Doc.cat(choices), Doc.render({
      style: "pretty",
      options: {
        lineWidth: columns
      }
    }));
  });
}
function renderSubmission(state, options) {
  return Effect.gen(function* () {
    const terminal = yield* Terminal.Terminal;
    const columns = yield* terminal.columns;
    const figures = yield* InternalAnsiUtils.figures;
    const selected = Doc.text(options.choices[state].title);
    const leadingSymbol = Doc.annotate(figures.tick, Ansi.green);
    const trailingSymbol = Doc.annotate(figures.ellipsis, Ansi.blackBright);
    const promptMsg = renderOutput(leadingSymbol, trailingSymbol, options);
    return promptMsg.pipe(Doc.cat(Doc.space), Doc.cat(Doc.annotate(selected, Ansi.white)), Doc.cat(Doc.hardLine), Doc.render({
      style: "pretty",
      options: {
        lineWidth: columns
      }
    }));
  });
}
function processCursorUp(state, choices) {
  if (state === 0) {
    return Effect.succeed(_action.Action.NextFrame({
      state: choices.length - 1
    }));
  }
  return Effect.succeed(_action.Action.NextFrame({
    state: state - 1
  }));
}
function processCursorDown(state, choices) {
  if (state === choices.length - 1) {
    return Effect.succeed(_action.Action.NextFrame({
      state: 0
    }));
  }
  return Effect.succeed(_action.Action.NextFrame({
    state: state + 1
  }));
}
function processNext(state, choices) {
  return Effect.succeed(_action.Action.NextFrame({
    state: (state + 1) % choices.length
  }));
}
function handleRender(options) {
  return (state, action) => {
    return _action.Action.$match(action, {
      Beep: () => Effect.succeed(renderBeep),
      NextFrame: ({
        state
      }) => renderNextFrame(state, options),
      Submit: () => renderSubmission(state, options)
    });
  };
}
function handleClear(options) {
  return Effect.gen(function* () {
    const terminal = yield* Terminal.Terminal;
    const columns = yield* terminal.columns;
    const clearPrompt = Doc.cat(Doc.eraseLine, Doc.cursorLeft);
    const text = "\n".repeat(Math.min(options.choices.length, options.maxPerPage)) + options.message;
    const clearOutput = InternalAnsiUtils.eraseText(text, columns);
    return clearOutput.pipe(Doc.cat(clearPrompt), Optimize.optimize(Optimize.Deep), Doc.render({
      style: "pretty",
      options: {
        lineWidth: columns
      }
    }));
  });
}
function handleProcess(options) {
  return (input, state) => {
    switch (input.key.name) {
      case "k":
      case "up":
        {
          return processCursorUp(state, options.choices);
        }
      case "j":
      case "down":
        {
          return processCursorDown(state, options.choices);
        }
      case "tab":
        {
          return processNext(state, options.choices);
        }
      case "enter":
      case "return":
        {
          const selected = options.choices[state];
          if (selected.disabled) {
            return Effect.succeed(_action.Action.Beep());
          }
          return Effect.succeed(_action.Action.Submit({
            value: selected.value
          }));
        }
      default:
        {
          return Effect.succeed(_action.Action.Beep());
        }
    }
  };
}
/** @internal */
const select = options => {
  const opts = {
    maxPerPage: 10,
    ...options
  };
  // Validate and seed initial index from any choice marked selected: true
  let initialIndex = 0;
  let seenSelected = -1;
  for (let i = 0; i < opts.choices.length; i++) {
    const choice = opts.choices[i];
    if (choice.selected === true) {
      if (seenSelected !== -1) {
        throw new Error("InvalidArgumentException: only a single choice can be selected by default for Prompt.select");
      }
      seenSelected = i;
    }
  }
  if (seenSelected !== -1) {
    initialIndex = seenSelected;
  }
  return InternalPrompt.custom(initialIndex, {
    render: handleRender(opts),
    process: handleProcess(opts),
    clear: () => handleClear(opts)
  });
};
exports.select = select;
//# sourceMappingURL=select.js.map