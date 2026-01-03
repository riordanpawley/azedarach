"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.list = void 0;
var InternalPrompt = _interopRequireWildcard(require("../prompt.js"));
var InternalTextPrompt = _interopRequireWildcard(require("./text.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/** @internal */
const list = options => InternalTextPrompt.text(options).pipe(InternalPrompt.map(output => output.split(options.delimiter || ",")));
exports.list = list;
//# sourceMappingURL=list.js.map