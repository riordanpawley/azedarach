"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.eraseText = eraseText;
exports.figures = void 0;
exports.lines = lines;
var Doc = _interopRequireWildcard(require("@effect/printer-ansi/AnsiDoc"));
var Arr = _interopRequireWildcard(require("effect/Array"));
var Effect = _interopRequireWildcard(require("effect/Effect"));
var _Function = require("effect/Function");
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
const defaultFigures = {
  arrowUp: /*#__PURE__*/Doc.text("↑"),
  arrowDown: /*#__PURE__*/Doc.text("↓"),
  arrowLeft: /*#__PURE__*/Doc.text("←"),
  arrowRight: /*#__PURE__*/Doc.text("→"),
  radioOn: /*#__PURE__*/Doc.text("◉"),
  radioOff: /*#__PURE__*/Doc.text("◯"),
  checkboxOn: /*#__PURE__*/Doc.text("☒"),
  checkboxOff: /*#__PURE__*/Doc.text("☐"),
  tick: /*#__PURE__*/Doc.text("✔"),
  cross: /*#__PURE__*/Doc.text("✖"),
  ellipsis: /*#__PURE__*/Doc.text("…"),
  pointerSmall: /*#__PURE__*/Doc.text("›"),
  line: /*#__PURE__*/Doc.text("─"),
  pointer: /*#__PURE__*/Doc.text("❯")
};
const windowsFigures = {
  arrowUp: defaultFigures.arrowUp,
  arrowDown: defaultFigures.arrowDown,
  arrowLeft: defaultFigures.arrowLeft,
  arrowRight: defaultFigures.arrowRight,
  radioOn: /*#__PURE__*/Doc.text("(*)"),
  radioOff: /*#__PURE__*/Doc.text("( )"),
  checkboxOn: /*#__PURE__*/Doc.text("[*]"),
  checkboxOff: /*#__PURE__*/Doc.text("[ ]"),
  tick: /*#__PURE__*/Doc.text("√"),
  cross: /*#__PURE__*/Doc.text("×"),
  ellipsis: /*#__PURE__*/Doc.text("..."),
  pointerSmall: /*#__PURE__*/Doc.text("»"),
  line: /*#__PURE__*/Doc.text("─"),
  pointer: /*#__PURE__*/Doc.text(">")
};
/** @internal */
const figures = exports.figures = /*#__PURE__*/Effect.map(/*#__PURE__*/Effect.sync(() => process.platform === "win32"), isWindows => isWindows ? windowsFigures : defaultFigures);
/**
 * Clears all lines taken up by the specified `text`.
 *
 * @internal
 */
function eraseText(text, columns) {
  if (columns === 0) {
    return Doc.cat(Doc.eraseLine, Doc.cursorTo(0));
  }
  let rows = 0;
  const lines = text.split(/\r?\n/);
  for (const line of lines) {
    rows += 1 + Math.floor(Math.max(line.length - 1, 0) / columns);
  }
  return Doc.eraseLines(rows);
}
/** @internal */
function lines(prompt, columns) {
  const lines = prompt.split(/\r?\n/);
  return columns === 0 ? lines.length : (0, _Function.pipe)(Arr.map(lines, line => Math.ceil(line.length / columns)), Arr.reduce(0, (left, right) => left + right));
}
//# sourceMappingURL=ansi-utils.js.map