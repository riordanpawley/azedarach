"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.confirm = void 0;
var Terminal = _interopRequireWildcard(require("@effect/platform/Terminal"));
var Ansi = _interopRequireWildcard(require("@effect/printer-ansi/Ansi"));
var Doc = _interopRequireWildcard(require("@effect/printer-ansi/AnsiDoc"));
var Optimize = _interopRequireWildcard(require("@effect/printer/Optimize"));
var Arr = _interopRequireWildcard(require("effect/Array"));
var Effect = _interopRequireWildcard(require("effect/Effect"));
var Option = _interopRequireWildcard(require("effect/Option"));
var InternalPrompt = _interopRequireWildcard(require("../prompt.js"));
var _action = require("./action.js");
var InternalAnsiUtils = _interopRequireWildcard(require("./ansi-utils.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
const renderBeep = /*#__PURE__*/Doc.render(Doc.beep, {
  style: "pretty"
});
function handleClear(options) {
  return Effect.gen(function* () {
    const terminal = yield* Terminal.Terminal;
    const columns = yield* terminal.columns;
    const clearOutput = InternalAnsiUtils.eraseText(options.message, columns);
    const resetCurrentLine = Doc.cat(Doc.eraseLine, Doc.cursorLeft);
    return clearOutput.pipe(Doc.cat(resetCurrentLine), Optimize.optimize(Optimize.Deep), Doc.render({
      style: "pretty",
      options: {
        lineWidth: columns
      }
    }));
  });
}
const NEWLINE_REGEX = /\r?\n/;
function renderOutput(confirm, leadingSymbol, trailingSymbol, options) {
  const annotateLine = line => Doc.annotate(Doc.text(line), Ansi.bold);
  const prefix = Doc.cat(leadingSymbol, Doc.space);
  return Arr.match(options.message.split(NEWLINE_REGEX), {
    onEmpty: () => Doc.hsep([prefix, trailingSymbol, confirm]),
    onNonEmpty: promptLines => {
      const lines = Arr.map(promptLines, line => annotateLine(line));
      return prefix.pipe(Doc.cat(Doc.nest(Doc.vsep(lines), 2)), Doc.cat(Doc.space), Doc.cat(trailingSymbol), Doc.cat(Doc.space), Doc.cat(confirm));
    }
  });
}
function renderNextFrame(state, options) {
  return Effect.gen(function* () {
    const terminal = yield* Terminal.Terminal;
    const columns = yield* terminal.columns;
    const figures = yield* InternalAnsiUtils.figures;
    const leadingSymbol = Doc.annotate(Doc.text("?"), Ansi.cyanBright);
    const trailingSymbol = Doc.annotate(figures.pointerSmall, Ansi.blackBright);
    // Marking these explicitly as present with `!` because they always will be
    // and there is really no value in adding a `DeepRequired` type helper just
    // for these internal cases
    const confirmMessage = state.value ? options.placeholder.defaultConfirm : options.placeholder.defaultDeny;
    const confirm = Doc.annotate(Doc.text(confirmMessage), Ansi.blackBright);
    const promptMsg = renderOutput(confirm, leadingSymbol, trailingSymbol, options);
    return Doc.cursorHide.pipe(Doc.cat(promptMsg), Optimize.optimize(Optimize.Deep), Doc.render({
      style: "pretty",
      options: {
        lineWidth: columns
      }
    }));
  });
}
function renderSubmission(value, options) {
  return Effect.gen(function* () {
    const terminal = yield* Terminal.Terminal;
    const columns = yield* terminal.columns;
    const figures = yield* InternalAnsiUtils.figures;
    const leadingSymbol = Doc.annotate(figures.tick, Ansi.green);
    const trailingSymbol = Doc.annotate(figures.ellipsis, Ansi.blackBright);
    const confirmMessage = value ? options.label.confirm : options.label.deny;
    const confirm = Doc.text(confirmMessage);
    const promptMsg = renderOutput(confirm, leadingSymbol, trailingSymbol, options);
    return promptMsg.pipe(Doc.cat(Doc.hardLine), Optimize.optimize(Optimize.Deep), Doc.render({
      style: "pretty",
      options: {
        lineWidth: columns
      }
    }));
  });
}
function handleRender(options) {
  return (_, action) => {
    return _action.Action.$match(action, {
      Beep: () => Effect.succeed(renderBeep),
      NextFrame: ({
        state
      }) => renderNextFrame(state, options),
      Submit: ({
        value
      }) => renderSubmission(value, options)
    });
  };
}
const TRUE_VALUE_REGEX = /^y|t$/;
const FALSE_VALUE_REGEX = /^n|f$/;
function handleProcess(input, defaultValue) {
  const value = Option.getOrElse(input.input, () => "");
  if (input.key.name === "enter" || input.key.name === "return") {
    return Effect.succeed(_action.Action.Submit({
      value: defaultValue
    }));
  }
  if (TRUE_VALUE_REGEX.test(value.toLowerCase())) {
    return Effect.succeed(_action.Action.Submit({
      value: true
    }));
  }
  if (FALSE_VALUE_REGEX.test(value.toLowerCase())) {
    return Effect.succeed(_action.Action.Submit({
      value: false
    }));
  }
  return Effect.succeed(_action.Action.Beep());
}
/** @internal */
const confirm = options => {
  const opts = {
    initial: false,
    ...options,
    label: {
      confirm: "yes",
      deny: "no",
      ...options.label
    },
    placeholder: {
      defaultConfirm: "(Y/n)",
      defaultDeny: "(y/N)",
      ...options.placeholder
    }
  };
  const initialState = {
    value: opts.initial
  };
  return InternalPrompt.custom(initialState, {
    render: handleRender(opts),
    process: input => handleProcess(input, opts.initial),
    clear: () => handleClear(opts)
  });
};
exports.confirm = confirm;
//# sourceMappingURL=confirm.js.map